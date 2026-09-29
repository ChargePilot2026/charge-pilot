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
type ExportTask struct {
	Store     ResourceStore
	Auth      API
	ExportDir string
	MaxRows   int
}

type exportRow struct {
	ID          uint64  `json:"id"`
	TaskNo      string  `json:"task_no" gorm:"column:task_no"`
	Resource    string  `json:"resource" gorm:"column:resource"`
	FileFormat  string  `json:"file_format" gorm:"column:file_format"`
	Status      string  `json:"status" gorm:"column:status"`
	RequestedBy uint64  `json:"requested_by" gorm:"column:requested_by"`
	FilterJSON  *string `json:"filter_json" gorm:"column:filter_json"`
	RowCount    uint32  `json:"row_count" gorm:"column:row_count"`
	FilePath    *string `json:"-" gorm:"column:file_path"`
	ErrorMsg    *string `json:"error_msg" gorm:"column:error_msg"`
	ExpiresAt   *string `json:"expires_at" gorm:"column:expires_at"`
	CreatedAt   string  `json:"created_at" gorm:"column:created_at"`
	CompletedAt *string `json:"completed_at" gorm:"column:completed_at"`
}

func (exportRow) TableName() string { return "export_task" }

func (t ExportTask) maxRows() int {
	if t.MaxRows > 0 {
		return t.MaxRows
	}
	return 50000
}

func (t ExportTask) exportDir() string {
	if t.ExportDir != "" {
		return t.ExportDir
	}
	return filepath.Join(os.TempDir(), "chargepilot-exports")
}

// CleanupExpired removes finished files after the 24-hour download window.
// The expected path is recomputed rather than trusting a mutable DB value.
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
type exportSpec struct {
	Permission string
	Table      string
	Columns    []exportColumn
	Builder    func(ctx context.Context, q *gorm.DB, scope DataScope) *gorm.DB
}

type exportColumn struct {
	Header string
	Column string
}

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

func (t ExportTask) register(r *gin.Engine) {
	r.GET("/api/v1/admin/exports", t.Auth.Require("finance.read"), t.listTasks)
	r.GET("/api/v1/admin/exports/:id", t.Auth.Require("finance.read"), t.detailTask)
	r.POST("/api/v1/admin/exports", t.Auth.Require("export.create"), t.createTask)
	r.GET("/api/v1/admin/exports/:id/download", t.Auth.Require("export.create"), t.downloadTask)
	r.GET("/api/v1/admin/exports/resources", t.Auth.Require("export.create"), t.listResources)
}

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

func exportRequiresGlobalScope(resource string) bool {
	return resource == "bills" || resource == "reconciles" || resource == "settlements"
}

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

func (t ExportTask) createTask(c *gin.Context) {
	var in struct {
		RequestID string         `json:"request_id"`
		Resource  string         `json:"resource"`
		Format    string         `json:"format"`
		Filter    map[string]any `json:"filter"`
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

func (t ExportTask) fail(ctx context.Context, taskNo string, cause error) error {
	_ = t.Store.AdminDB.WithContext(ctx).Model(&exportRow{}).Where("task_no = ?", taskNo).
		Updates(map[string]any{"status": "failed", "error_msg": truncate(cause.Error(), 255)}).Error
	return cause
}

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

func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

// downloadTask serves a finished export. Ownership and expiry are re-checked on
// every download so revoking an account immediately revokes its files.
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
