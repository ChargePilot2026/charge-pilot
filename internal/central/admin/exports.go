package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ExportTask builds bounded CSV, XLSX or summary PDF files. Exports are permission checked
// when they are requested and again when they are downloaded, because a file
// must not stay readable after the requesting account loses its role.
// ExportTask 是导出能力的一站式入口:申请时按资源权限和数据范围过滤,
// 生成 CSV/XLSX/PDF 落到导出目录,下载时再校验一次权限与有效期,过期后由定时任务清理。
type ExportTask struct {
	Store     ResourceStore // 数据库连接集合,导出时按资源选择用户库/管理库/计费库
	Auth      API           // 鉴权器,申请与下载两个动作都走它的权限校验
	ExportDir string        // 导出文件目录,留空则用系统临时目录下的 chargepilot-exports
	MaxRows   int           // 单次导出行数上限,留空默认 50000,超出直接判失败而不是截断
}

// exportRow 对应管理库 export_task 表的一行,记录一次导出申请从申请到过期的一生。
type exportRow struct {
	ID          uint64  `json:"id"`                                      // 导出任务主键 ID
	TaskNo      string  `json:"task_no" gorm:"column:task_no"`           // 任务号,形如 EXP + 32 位 UUID(大写无横线),同时用作文件名
	Resource    string  `json:"resource" gorm:"column:resource"`         // 导出资源名,取自 exportRegistry:orders/stations/devices/settlements/bills/reconciles
	FileFormat  string  `json:"file_format" gorm:"column:file_format"`   // 文件格式:csv / xlsx / pdf;PDF 只支持 bills 与 reconciles
	Status      string  `json:"status" gorm:"column:status"`             // 任务状态:pending 待生成 / running 生成中 / completed 已完成 / failed 失败 / expired 已过期
	RequestedBy uint64  `json:"requested_by" gorm:"column:requested_by"` // 发起导出的管理员 ID,下载时用于校验归属
	FilterJSON  *string `json:"filter_json" gorm:"column:filter_json"`   // 筛选条件 JSON 原文(可空),如 from/to 日期区间
	RowCount    uint32  `json:"row_count" gorm:"column:row_count"`       // 实际导出行数,生成完成时写入
	FilePath    *string `json:"-" gorm:"column:file_path"`               // 服务器上的文件路径,不返回给前端(json:"-"),只在下载与清理时内部使用
	ErrorMsg    *string `json:"error_msg" gorm:"column:error_msg"`       // 失败原因(可空),最长 255 字符
	ExpiresAt   *string `json:"expires_at" gorm:"column:expires_at"`     // 下载截止时间,创建时为 24 小时后
	CreatedAt   string  `json:"created_at" gorm:"column:created_at"`     // 申请时间,UTC
	CompletedAt *string `json:"completed_at" gorm:"column:completed_at"` // 生成完成时间(可空)
}

// TableName 指定 GORM 映射到管理库 export_task 表。
func (exportRow) TableName() string { return "export_task" }

// maxRows 返回单次导出的行数上限,未配置时回落到 50000;取数会多取一行用来判断是否超限。
func (t ExportTask) maxRows() int {
	if t.MaxRows > 0 {
		return t.MaxRows
	}
	return 50000
}

// exportDir 返回导出文件目录,未配置时使用系统临时目录下的 chargepilot-exports。
func (t ExportTask) exportDir() string {
	if t.ExportDir != "" {
		return t.ExportDir
	}
	return filepath.Join(os.TempDir(), "chargepilot-exports")
}

// CleanupExpired removes finished files after the 24-hour download window.
// The expected path is recomputed rather than trusting a mutable DB value.
// CleanupExpired 清理已过期任务:删掉磁盘文件并把状态置为 expired,单次最多处理 1000 条,
// 由定时任务周期调用;文件路径是重新算出来的,不信任库里那个可被改写的值。
func (t ExportTask) CleanupExpired(ctx context.Context) (int, error) {
	rows := []exportRow{}
	if err := t.Store.AdminDB.WithContext(ctx).Table("export_task").
		Where("status IN ? AND expires_at <= ?", []string{"completed", "failed"}, time.Now().UTC()).
		Order("id").Limit(1000).Find(&rows).Error; err != nil {
		return 0, err
	}
	removed := 0
	for _, row := range rows {
		format := row.FileFormat
		if format == "" {
			format = "csv"
		}
		expected := filepath.Join(t.exportDir(), row.TaskNo+"."+format)
		if row.FilePath != nil && *row.FilePath == expected && filepath.Base(row.TaskNo) == row.TaskNo {
			if err := os.Remove(expected); err != nil && !errors.Is(err, os.ErrNotExist) {
				return removed, err
			}
		}
		if err := t.Store.AdminDB.WithContext(ctx).Table("export_task").Where("id = ? AND status IN ?", row.ID, []string{"completed", "failed"}).Update("status", "expired").Error; err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// exportSpec declares the columns and query for one resource, so adding a
// resource cannot accidentally expose an unmasked column.
// exportSpec 描述一个可导出资源:需要什么权限、数据在哪张表、导出哪些列,以及怎么套用数据范围。
// 新增资源必须在这里登记,避免漏做权限校验或把未脱敏的列带出去。
type exportSpec struct {
	Permission string                                                          // 导出该资源所需的读权限,申请和下载时都会校验
	Table      string                                                          // 数据来源表名,据此选择用户库/管理库/计费库连接
	Columns    []exportColumn                                                  // 导出列清单,顺序即文件列顺序;Header 是中文表头,Column 是数据库列名
	Builder    func(ctx context.Context, q *gorm.DB, scope DataScope) *gorm.DB // 在基础查询上追加数据范围等条件的钩子,为空表示不额外过滤
}

// exportColumn 是一列的表头与数据库列名映射,两者分开,表头才能用中文。
type exportColumn struct {
	Header string // 文件里的中文表头
	Column string // 数据库列名,也是字段脱敏规则匹配用的字段名
}

// exportRegistry 是可导出资源的白名单:键是资源名,值是它的权限、表、列和过滤钩子。
// 没登记的资源无法被申请,运营也就没法临时拼 SQL 导出未脱敏字段。
var exportRegistry = map[string]exportSpec{
	"orders": {
		Permission: "order.read",
		Table:      "charge_order",
		Columns: []exportColumn{
			{"订单号", "order_no"}, {"用户", "user_id"}, {"设备", "device_id"}, {"端口", "port_no"},
			{"状态", "status"}, {"开始时间", "started_at"}, {"结束时间", "ended_at"},
			{"电费(分)", "electric_cents"}, {"服务费(分)", "service_cents"}, {"合计(分)", "total_cents"},
		},
		Builder: func(ctx context.Context, q *gorm.DB, scope DataScope) *gorm.DB {
			return scope.ApplyStations(q, "station_id")
		},
	},
	"stations": {
		Permission: "station.read",
		Table:      "station",
		Columns: []exportColumn{
			{"编码", "code"}, {"名称", "name"}, {"地址", "address"}, {"状态", "status"},
		},
		Builder: func(ctx context.Context, q *gorm.DB, scope DataScope) *gorm.DB {
			return scope.ApplyStations(q, "id")
		},
	},
	"devices": {
		Permission: "device.read",
		Table:      "device_meta",
		Columns: []exportColumn{
			{"设备号", "device_id"}, {"型号", "model"}, {"站点", "station_id"}, {"厂商", "vendor_id"}, {"状态", "status"},
		},
		Builder: func(ctx context.Context, q *gorm.DB, scope DataScope) *gorm.DB {
			return scope.ApplyStations(q, "station_id")
		},
	},
	"settlements": {
		Permission: "finance.read",
		Table:      "settlement",
		Columns: []exportColumn{
			{"分账单号", "settlement_no"}, {"订单号", "order_no"}, {"模式", "mode"}, {"状态", "status"},
			{"合计(分)", "total_cents"}, {"分账池(分)", "split_pool_cents"},
		},
		Builder: func(ctx context.Context, q *gorm.DB, scope DataScope) *gorm.DB { return q },
	},
	"bills": {
		Permission: "finance.read",
		Table:      "charge_bill",
		Columns: []exportColumn{
			{"账单号", "bill_no"}, {"订单ID", "charge_order_id"}, {"用户ID", "user_id"},
			{"设备号", "device_id"}, {"电费(分)", "electric_cents"}, {"服务费(分)", "service_cents"},
			{"合计(分)", "total_cents"}, {"退款(分)", "refund_cents"}, {"状态", "status"}, {"开具时间", "issued_at"},
		},
	},
	"reconciles": {
		Permission: "finance.read",
		Table:      "finance_reconcile_log",
		Columns: []exportColumn{
			{"对账类型", "reconcile_type"}, {"对账日期", "reconcile_date"},
			{"内部笔数", "internal_count"}, {"渠道笔数", "wechat_count"}, {"差异笔数", "diff_count"},
			{"内部金额(分)", "internal_cents"}, {"渠道金额(分)", "wechat_cents"}, {"差额(分)", "diff_cents"},
			{"已解决", "resolved"},
		},
	},
}

// register 注册导出路由:申请与下载需要 export.create,列表与详情需要 finance.read。
func (t ExportTask) register(r *gin.Engine) {
	r.GET("/api/v1/admin/exports", t.Auth.Require("finance.read"), t.listTasks)
	r.GET("/api/v1/admin/exports/:id", t.Auth.Require("finance.read"), t.detailTask)
	r.POST("/api/v1/admin/exports", t.Auth.Require("export.create"), t.createTask)
	r.GET("/api/v1/admin/exports/:id/download", t.Auth.Require("export.create"), t.downloadTask)
	r.GET("/api/v1/admin/exports/resources", t.Auth.Require("export.create"), t.listResources)
}

// detailTask 返回单个导出任务的状态且不含文件路径;能不能下载由下载接口单独判定。
func (t ExportTask) detailTask(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var row exportRow
	if err := t.Store.AdminDB.WithContext(c.Request.Context()).Table("export_task").Where("id = ?", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	// exportRow.FilePath is excluded from JSON. Reading task status does not
	// grant access to the file; download rechecks creator and resource rights.
	httpapi.OK(c, row)
}

// listResources 返回当前管理员可导出的资源与格式清单,并带上行数上限,
// 前端据此渲染导出选项,避免给出点了必然 403 的组合。
func (t ExportTask) listResources(c *gin.Context) {
	profile := c.MustGet("admin_profile").(Profile)
	scope, err := LoadDataScope(c.Request.Context(), t.Store.AdminDB, profile)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	items := []string{}
	for name, spec := range exportRegistry {
		if hasPermission(profile, spec.Permission) && (scope.Unrestricted || !exportRequiresGlobalScope(name)) {
			items = append(items, name)
		}
	}
	httpapi.OK(c, gin.H{"items": items, "formats": []string{"csv", "xlsx", "pdf"}, "pdf_resources": []string{"bills", "reconciles"}, "max_rows": t.maxRows()})
}

// exportRequiresGlobalScope 判断该资源是否只允许全局数据范围的管理员导出。
// 财务类资源(bills/reconciles/settlements)天然跨站点,限定站点范围的账号拿不到全量,故直接禁掉。
func exportRequiresGlobalScope(resource string) bool {
	return resource == "bills" || resource == "reconciles" || resource == "settlements"
}

// listTasks 分页返回导出任务,支持按状态过滤,按 ID 倒序。
func (t ExportTask) listTasks(c *gin.Context) {
	page, ok := parsePage(c, "pending running completed failed expired")
	if !ok {
		return
	}
	out := Page[exportRow]{Items: []exportRow{}, Page: page.Page, PageSize: page.PageSize}
	query := t.Store.AdminDB.WithContext(c.Request.Context()).Table("export_task")
	if page.Status != "" {
		query = query.Where("status = ?", page.Status)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := query.Order("id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// createTask 受理一次导出申请:校验请求号、资源、格式、权限与数据范围后落一条 pending 任务,
// 随后同步生成文件并直接返回结果;相同请求号重放返回原任务,条件不一致则报冲突。
func (t ExportTask) createTask(c *gin.Context) {
	var in struct {
		RequestID string         `json:"request_id"` // 客户端请求号,必须是 UUID,用于幂等
		Resource  string         `json:"resource"`   // 要导出的资源名,必须已在 exportRegistry 登记
		Format    string         `json:"format"`     // 文件格式,留空按 csv;pdf 只支持 bills 与 reconciles
		Filter    map[string]any `json:"filter"`     // 筛选条件,bills/reconciles 必填 from/to 且跨度不超过 31 天
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validUUID(in.RequestID) {
		httpapi.BadRequest(c, "请提供 UUID 格式的请求号")
		return
	}
	spec, known := exportRegistry[in.Resource]
	if !known {
		httpapi.BadRequest(c, "不支持的导出资源")
		return
	}
	if in.Format == "" {
		in.Format = "csv"
	}
	if in.Format != "csv" && in.Format != "xlsx" && in.Format != "pdf" {
		httpapi.BadRequest(c, "导出格式无效")
		return
	}
	if in.Format == "pdf" && in.Resource != "bills" && in.Resource != "reconciles" {
		httpapi.BadRequest(c, "PDF 仅支持账单与对账汇总")
		return
	}
	if (in.Resource == "bills" || in.Resource == "reconciles") && !validExportPeriod(in.Filter) {
		httpapi.BadRequest(c, "账单与对账需提供不超过 31 天的 from/to 日期")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	if !hasPermission(profile, spec.Permission) {
		httpapi.Write(c, 403, 1003, "没有导出该资源的权限", nil)
		return
	}
	if exportRequiresGlobalScope(in.Resource) {
		scope, err := LoadDataScope(c.Request.Context(), t.Store.AdminDB, profile)
		if err != nil {
			resourceFailure(c, err)
			return
		}
		if !scope.Unrestricted {
			httpapi.Write(c, 403, 1003, "该财务资源只允许全局数据范围导出", nil)
			return
		}
	}
	// Keep all 128 UUID bits. Truncating the trailing bytes made requests with
	// the same prefix collide even though their request IDs were distinct.
	taskNo := "EXP" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	filter, err := json.Marshal(in.Filter)
	if err != nil {
		httpapi.BadRequest(c, "导出筛选条件无效")
		return
	}
	var replayed *exportRow
	err = t.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var existing exportRow
		found := tx.Table("export_task").Where("task_no = ?", taskNo).Take(&existing)
		if found.Error == nil {
			var savedFilter map[string]any
			if existing.FilterJSON != nil {
				_ = json.Unmarshal([]byte(*existing.FilterJSON), &savedFilter)
			}
			if existing.RequestedBy != profile.ID || existing.Resource != in.Resource || existing.FileFormat != in.Format || !reflect.DeepEqual(savedFilter, in.Filter) {
				return errConflict
			}
			replayed = &existing
			return nil
		}
		if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		if err := tx.Table("export_task").Create(map[string]any{
			"task_no": taskNo, "resource": in.Resource, "file_format": in.Format, "status": "pending", "requested_by": profile.ID,
			"filter_json": string(filter), "expires_at": time.Now().UTC().Add(24 * time.Hour),
		}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "create", "export_task", 0, nil, gin.H{"resource": in.Resource, "task_no": taskNo}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if replayed != nil {
		httpapi.OK(c, gin.H{"task_no": taskNo, "status": replayed.Status, "row_count": replayed.RowCount, "expires_at": replayed.ExpiresAt})
		return
	}
	// Build synchronously: exports are small and bounded, and returning a ready
	// file keeps the operator flow to one step instead of polling.
	if err := t.run(c.Request.Context(), taskNo, spec, profile); err != nil {
		httpapi.OK(c, gin.H{"task_no": taskNo, "status": "failed", "message": "导出失败，请稍后重试"})
		return
	}
	var finished exportRow
	t.Store.AdminDB.WithContext(c.Request.Context()).Table("export_task").Where("task_no = ?", taskNo).Take(&finished)
	httpapi.OK(c, gin.H{"task_no": taskNo, "status": finished.Status, "row_count": finished.RowCount, "expires_at": finished.ExpiresAt})
}

// run writes the requested format and records the outcome. The file is written to a
// temporary name and renamed, so a partial export is never downloadable.
// run 真正执行导出:按资源选库、套数据范围与日期区间、取数、脱敏,再写成目标格式,
// 先写 .partial 再改名,保证下载端永远拿不到半截文件;任一步失败都把任务置为 failed。
func (t ExportTask) run(ctx context.Context, taskNo string, spec exportSpec, profile Profile) error {
	dir := t.exportDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return t.fail(ctx, taskNo, err)
	}
	var row exportRow
	if err := t.Store.AdminDB.WithContext(ctx).Table("export_task").Where("task_no = ?", taskNo).Take(&row).Error; err != nil {
		return err
	}
	scope, err := LoadDataScope(ctx, t.Store.AdminDB, profile)
	if err != nil {
		return t.fail(ctx, taskNo, err)
	}
	if exportRequiresGlobalScope(row.Resource) && !scope.Unrestricted {
		return t.fail(ctx, taskNo, errors.New("scoped finance export is not available"))
	}
	var filters map[string]any
	if row.FilterJSON != nil && *row.FilterJSON != "" {
		_ = json.Unmarshal([]byte(*row.FilterJSON), &filters)
	}
	if filters == nil {
		filters = map[string]any{}
	}
	query := t.Store.UserDB.WithContext(ctx).Table(spec.Table)
	if spec.Table == "station" || spec.Table == "device_meta" || spec.Table == "finance_reconcile_log" {
		query = t.Store.AdminDB.WithContext(ctx).Table(spec.Table)
	} else if spec.Table == "settlement" {
		query = t.Store.BillingDB.WithContext(ctx).Table(spec.Table)
	}
	if spec.Table != "charge_bill" && spec.Table != "finance_reconcile_log" && spec.Table != "settlement" {
		query = query.Where(map[string]any{"deleted_at": nil})
	}
	if spec.Table == "charge_bill" {
		query = query.Where("issued_at >= ? AND issued_at < ?", filters["from"], nextExportDate(filters["to"]))
	} else if spec.Table == "finance_reconcile_log" {
		query = query.Where("reconcile_date >= ? AND reconcile_date <= ?", filters["from"], filters["to"])
	}
	if spec.Builder != nil {
		query = spec.Builder(ctx, query, scope)
	}
	records := []map[string]any{}
	if err := query.Limit(t.maxRows() + 1).Find(&records).Error; err != nil {
		return t.fail(ctx, taskNo, err)
	}
	if len(records) > t.maxRows() {
		return t.fail(ctx, taskNo, fmt.Errorf("导出超过 %d 行，请缩小范围", t.maxRows()))
	}
	mask, err := LoadFieldMask(ctx, t.Store.AdminDB, profile.RoleID)
	if err != nil {
		return t.fail(ctx, taskNo, err)
	}
	names := make([]string, 0, len(spec.Columns))
	for _, column := range spec.Columns {
		names = append(names, column.Column)
	}
	records = mask.MaskSlice(spec.Table, records)

	format := row.FileFormat
	if format == "" {
		format = "csv"
	}
	path := filepath.Join(dir, taskNo+"."+format)
	pending := path + ".partial"
	file, err := os.OpenFile(pending, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return t.fail(ctx, taskNo, err)
	}
	headers := make([]string, 0, len(spec.Columns))
	for _, column := range spec.Columns {
		headers = append(headers, column.Header)
	}
	lines := make([][]string, 0, len(records))
	for _, record := range records {
		line := make([]string, 0, len(names))
		for _, name := range names {
			line = append(line, stringifyCell(record[name]))
		}
		lines = append(lines, line)
	}
	if err := writeExport(file, format, row.Resource, headers, lines, filters); err != nil {
		file.Close()
		os.Remove(pending)
		return t.fail(ctx, taskNo, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(pending)
		return t.fail(ctx, taskNo, err)
	}
	if err := os.Rename(pending, path); err != nil {
		os.Remove(pending)
		return t.fail(ctx, taskNo, err)
	}
	return t.Store.AdminDB.WithContext(ctx).Model(&exportRow{}).Where("task_no = ?", taskNo).
		Updates(map[string]any{"status": "completed", "row_count": uint32(len(records)), "file_path": path,
			"completed_at": time.Now().UTC()}).Error
}

// fail 把任务标记为失败并记录截断后的错误原因,同时把错误原样返回给调用方。
func (t ExportTask) fail(ctx context.Context, taskNo string, cause error) error {
	_ = t.Store.AdminDB.WithContext(ctx).Model(&exportRow{}).Where("task_no = ?", taskNo).
		Updates(map[string]any{"status": "failed", "error_msg": truncate(cause.Error(), 255)}).Error
	return cause
}

// stringifyCell 把驱动返回的任意值转成导出用字符串:nil 输出空串,[]byte 按文本解包。
func stringifyCell(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case []byte:
		return string(v)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// truncate 按字节截断字符串,用于把错误信息塞进长度受限的字段。
func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

// downloadTask serves a finished export. Ownership and expiry are re-checked on
// every download so revoking an account immediately revokes its files.
// downloadTask 发送导出文件:每次下载都重新校验归属、资源权限、数据范围和有效期,
// 过期时顺手删文件并置 expired,保证权限被收回后文件立刻不可达。
func (t ExportTask) downloadTask(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	var row exportRow
	if err := t.Store.AdminDB.WithContext(c.Request.Context()).Table("export_task").
		Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if row.RequestedBy != profile.ID && !hasPermission(profile, "admin_user.read") {
		httpapi.Write(c, 403, 1003, "只能下载自己创建的导出文件", nil)
		return
	}
	spec, known := exportRegistry[row.Resource]
	if !known || !hasPermission(profile, spec.Permission) {
		// The permission that justified the export must still be held.
		httpapi.Write(c, 403, 1003, "没有下载该导出的权限", nil)
		return
	}
	if exportRequiresGlobalScope(row.Resource) {
		scope, err := LoadDataScope(c.Request.Context(), t.Store.AdminDB, profile)
		if err != nil {
			resourceFailure(c, err)
			return
		}
		if !scope.Unrestricted {
			httpapi.Write(c, 403, 1003, "当前数据范围无权下载该财务导出", nil)
			return
		}
	}
	if row.Status == "expired" {
		httpapi.Write(c, 410, 1005, "导出文件已过期，请重新导出", nil)
		return
	}
	if row.Status != "completed" || row.FilePath == nil {
		httpapi.Write(c, 409, 2009, "导出尚未完成", nil)
		return
	}
	if row.ExpiresAt != nil && *row.ExpiresAt != "" {
		expiry, err := time.Parse("2006-01-02 15:04:05.999", *row.ExpiresAt)
		if err == nil && time.Now().UTC().After(expiry) {
			_ = t.Store.AdminDB.WithContext(c.Request.Context()).Model(&exportRow{}).
				Where("task_no = ?", row.TaskNo).Update("status", "expired").Error
			_ = os.Remove(*row.FilePath)
			httpapi.Write(c, 410, 1005, "导出文件已过期，请重新导出", nil)
			return
		}
	}
	format := row.FileFormat
	if format == "" {
		format = "csv"
	}
	mime := map[string]string{"csv": "text/csv; charset=utf-8", "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "pdf": "application/pdf"}[format]
	if mime == "" {
		httpapi.Write(c, 409, 2009, "导出格式不可用", nil)
		return
	}
	c.Header("Content-Type", mime)
	c.Header("Content-Disposition", `attachment; filename="`+row.TaskNo+`.`+format+`"`)
	c.File(*row.FilePath)
}
