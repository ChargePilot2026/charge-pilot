package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ExportTask builds a CSV off the request path. Exports are permission checked
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
	items := []string{}
	for name, spec := range exportRegistry {
		if hasPermission(profile, spec.Permission) {
			items = append(items, name)
		}
	}
	httpapi.OK(c, gin.H{"items": items, "max_rows": t.maxRows()})
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
	profile := c.MustGet("admin_profile").(Profile)
	if !hasPermission(profile, spec.Permission) {
		httpapi.Write(c, 403, 1003, "没有导出该资源的权限", nil)
		return
	}
	taskNo := "EXP" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))[:24]
	filter, err := json.Marshal(in.Filter)
	if err != nil {
		httpapi.BadRequest(c, "导出筛选条件无效")
		return
	}
	err = t.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var existing exportRow
		found := tx.Table("export_task").Where("task_no = ?", taskNo).Take(&existing)
		if found.Error == nil {
			if existing.Resource != in.Resource {
				return errConflict
			}
			return nil
		}
		if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		if err := tx.Table("export_task").Create(map[string]any{
			"task_no": taskNo, "resource": in.Resource, "status": "pending", "requested_by": profile.ID,
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

// run writes the CSV and records the outcome. The file is written to a
// temporary name and renamed, so a partial export is never downloadable.
func (t ExportTask) run(ctx context.Context, taskNo string, spec exportSpec, profile Profile) error {
	dir := t.ExportDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "chargepilot-exports")
	}
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
	var filters map[string]any
	if row.FilterJSON != nil && *row.FilterJSON != "" {
		_ = json.Unmarshal([]byte(*row.FilterJSON), &filters)
	}
	query := t.Store.UserDB.WithContext(ctx).Table(spec.Table)
	if spec.Table == "station" || spec.Table == "device_meta" {
		query = t.Store.AdminDB.WithContext(ctx).Table(spec.Table)
	} else if spec.Table == "settlement" {
		query = t.Store.BillingDB.WithContext(ctx).Table(spec.Table)
	}
	query = query.Where(map[string]any{"deleted_at": nil})
	if spec.Builder != nil {
		query = spec.Builder(ctx, query, scope)
	}
	records := []map[string]any{}
	if err := query.Limit(t.maxRows()).Find(&records).Error; err != nil {
		return t.fail(ctx, taskNo, err)
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

	path := filepath.Join(dir, taskNo+".csv")
	pending := path + ".partial"
	file, err := os.OpenFile(pending, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return t.fail(ctx, taskNo, err)
	}
	writer := csv.NewWriter(file)
	headers := make([]string, 0, len(spec.Columns))
	for _, column := range spec.Columns {
		headers = append(headers, column.Header)
	}
	if err := writer.Write(headers); err != nil {
		file.Close()
		os.Remove(pending)
		return t.fail(ctx, taskNo, err)
	}
	for _, record := range records {
		line := make([]string, 0, len(names))
		for _, name := range names {
			line = append(line, stringifyCell(record[name]))
		}
		if err := writer.Write(line); err != nil {
			file.Close()
			os.Remove(pending)
			return t.fail(ctx, taskNo, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
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
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+row.TaskNo+`.csv"`)
	c.File(*row.FilePath)
}
