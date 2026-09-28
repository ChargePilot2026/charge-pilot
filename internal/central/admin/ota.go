package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OtaAPI manages firmware packages and rollout plans. admin_db owns the schedule
// and the package metadata; the device push and its acknowledgement live in
// gateway_db and are driven through the service-token internal API.
type OtaAPI struct {
	Store                    ResourceStore
	Auth                     API
	GatewayURL, ServiceToken string
}

type otaPackage struct {
	ID             uint64  `json:"id"`
	Code           string  `json:"code"`
	VendorID       *uint64 `json:"vendor_id"`
	Version        string  `json:"version"`
	StorageURL     string  `json:"storage_url"`
	SizeBytes      uint64  `json:"size_bytes"`
	ChecksumSHA256 string  `json:"checksum_sha256"`
	Sign           *string `json:"sign"`
	ReleaseNotes   *string `json:"release_notes"`
	Status         string  `json:"status"`
	CreatedAt      string  `json:"created_at"`
}

type otaSchedule struct {
	ID        uint64 `json:"id"`
	PackageID uint64 `json:"package_id"`
	// TargetFilter is decoded after the query; the column is JSON.
	TargetFilter    map[string]any `json:"target_filter" gorm:"-"`
	RolloutStrategy string         `json:"rollout_strategy"`
	BatchSize       *uint32        `json:"batch_size"`
	Status          string         `json:"status"`
	ScheduledAt     *string        `json:"scheduled_at"`
	StartedAt       *string        `json:"started_at"`
	CompletedAt     *string        `json:"completed_at"`
	Progress        map[string]any `json:"progress" gorm:"-"`
}

func (a OtaAPI) registerOta(r *gin.Engine) {
	r.GET("/api/v1/admin/ota/packages", a.Auth.Require("ota.read"), a.listPackages)
	r.POST("/api/v1/admin/ota/packages", a.Auth.Require("ota.package.create"), a.createPackage)
	r.DELETE("/api/v1/admin/ota/packages/:id", a.Auth.Require("ota.package.delete"), a.deletePackage)
	r.POST("/api/v1/admin/ota/schedules", a.Auth.Require("ota.schedule.create"), a.createSchedule)
	r.GET("/api/v1/admin/ota/schedules", a.Auth.Require("ota.read"), a.listSchedules)
	r.POST("/api/v1/admin/ota/schedules/:id/trigger", a.Auth.Require("ota.schedule.trigger"), a.triggerSchedule)
	r.POST("/api/v1/admin/ota/schedules/:id/cancel", a.Auth.Require("ota.schedule.trigger"), a.cancelSchedule)
}

var firmwareVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (a OtaAPI) listPackages(c *gin.Context) {
	page, ok := parsePage(c, "draft published archived")
	if !ok {
		return
	}
	out := Page[otaPackage]{Items: []otaPackage{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("ota_package").Where("deleted_at IS NULL")
	if page.Status != "" {
		query = query.Where("status = ?", page.Status)
	}
	if page.Keyword != "" {
		query = query.Where("code LIKE ? OR version LIKE ?", likePattern(page.Keyword), likePattern(page.Keyword))
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

// createPackage records firmware metadata. The artifact itself must already be
// in HTTPS storage; only a checksum-verifiable reference is accepted here.
func (a OtaAPI) createPackage(c *gin.Context) {
	var in struct {
		Code           string  `json:"code"`
		VendorID       *uint64 `json:"vendor_id"`
		Version        string  `json:"version"`
		StorageURL     string  `json:"storage_url"`
		SizeBytes      uint64  `json:"size_bytes"`
		ChecksumSHA256 string  `json:"checksum_sha256"`
		Sign           *string `json:"sign"`
		ReleaseNotes   *string `json:"release_notes"`
		Publish        bool    `json:"publish"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Code, 64) || !firmwareVersionPattern.MatchString(in.Version) || !httpsURL(in.StorageURL) {
		httpapi.BadRequest(c, "请填写有效的固件编码、版本和 HTTPS 下载地址")
		return
	}
	if in.SizeBytes == 0 || in.SizeBytes > 2<<30 {
		httpapi.BadRequest(c, "固件大小无效")
		return
	}
	if err := verifyChecksumFormat(in.ChecksumSHA256); err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	if in.Sign != nil && len(*in.Sign) > 512 {
		httpapi.BadRequest(c, "固件签名过长")
		return
	}
	if in.ReleaseNotes != nil && utf8.RuneCountInString(*in.ReleaseNotes) > 20000 {
		httpapi.BadRequest(c, "更新说明过长")
		return
	}
	status := "draft"
	if in.Publish {
		status = "published"
	}
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if in.VendorID != nil {
			var exists int64
			if err := tx.Table("vendor").Where("id = ? AND deleted_at IS NULL", *in.VendorID).Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				return errConflict
			}
		}
		if err := tx.Table("ota_package").Create(map[string]any{
			"code": in.Code, "vendor_id": in.VendorID, "version": in.Version, "storage_url": in.StorageURL,
			"size_bytes": in.SizeBytes, "checksum_sha256": strings.ToLower(in.ChecksumSHA256), "sign": in.Sign,
			"release_notes": in.ReleaseNotes, "status": status,
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "create", "ota_package", id, nil, gin.H{"code": in.Code, "version": in.Version}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": status})
}

func verifyChecksumFormat(value string) error {
	decoded, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(value)))
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("固件校验和必须是 64 位十六进制 sha256")
	}
	return nil
}

func (a OtaAPI) deletePackage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var running int64
		// A package that is mid-rollout cannot be withdrawn; devices would be
		// left pointing at firmware the backend no longer serves.
		if err := tx.Table("ota_schedule").Where("package_id = ? AND status = 'running'", id).Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			return errConflict
		}
		var before map[string]any
		if err := tx.Table("ota_package").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("ota_package").Where("id = ?", id).Updates(map[string]any{"deleted_at": time.Now().UTC(), "status": "archived"}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "delete", "ota_package", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "deleted": true})
}

func (a OtaAPI) createSchedule(c *gin.Context) {
	var in struct {
		PackageID       uint64         `json:"package_id"`
		TargetFilter    map[string]any `json:"target_filter"`
		RolloutStrategy string         `json:"rollout_strategy"`
		BatchSize       *uint32        `json:"batch_size"`
		ScheduledAt     *time.Time     `json:"scheduled_at"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.PackageID == 0 || !oneOf(in.RolloutStrategy, "all canary batch manual") {
		httpapi.BadRequest(c, "请选择有效固件包和发布策略")
		return
	}
	if in.RolloutStrategy == "canary" && (in.BatchSize == nil || *in.BatchSize < 1 || *in.BatchSize > 10000) {
		httpapi.BadRequest(c, "灰度发布需要 1–10000 的批次数量")
		return
	}
	filter, err := json.Marshal(in.TargetFilter)
	if err != nil {
		httpapi.BadRequest(c, "目标筛选条件无效")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	var id uint64
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var pkg struct {
			Status string
		}
		if err := tx.Table("ota_package").Where("id = ? AND deleted_at IS NULL", *&in.PackageID).Take(&pkg).Error; err != nil {
			return err
		}
		if pkg.Status != "published" {
			return errConflict
		}
		if err := tx.Table("ota_schedule").Create(map[string]any{
			"package_id": in.PackageID, "target_filter_json": string(filter), "rollout_strategy": in.RolloutStrategy,
			"batch_size": in.BatchSize, "status": "pending", "scheduled_at": in.ScheduledAt, "created_by": profile.ID,
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "create", "ota_schedule", id, nil, gin.H{"package_id": in.PackageID, "strategy": in.RolloutStrategy}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": "pending"})
}

func (a OtaAPI) listSchedules(c *gin.Context) {
	page, ok := parsePage(c, "pending running completed cancelled failed")
	if !ok {
		return
	}
	out := Page[otaSchedule]{Items: []otaSchedule{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("ota_schedule")
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
	for i := range out.Items {
		out.Items[i].Progress = a.scheduleProgress(c.Request.Context(), out.Items[i].ID)
		out.Items[i].TargetFilter = map[string]any{}
		var raw []byte
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("ota_schedule").
			Select("CAST(target_filter_json AS CHAR)").Where("id = ?", out.Items[i].ID).Scan(&raw).Error; err == nil && len(raw) > 0 {
			// The column is JSON, so it is read as text and decoded explicitly.
			_ = json.Unmarshal(raw, &out.Items[i].TargetFilter)
		}
	}
	httpapi.OK(c, out)
}

// scheduleProgress reports how many devices acknowledged the push, read from
// gateway through the internal status API rather than from admin_db.
func (a OtaAPI) scheduleProgress(ctx context.Context, scheduleID uint64) map[string]any {
	var row struct {
		PackageID  uint64  `gorm:"column:package_id"`
		TargetJSON *string `gorm:"column:target_filter_json"`
	}
	if err := a.Store.AdminDB.WithContext(ctx).Table("ota_schedule").Where("id = ?", scheduleID).Take(&row).Error; err != nil {
		return gin.H{"schedule_id": scheduleID, "available": false}
	}
	devices, err := a.targetDevicesByCode(ctx, row.TargetJSON, nil)
	if err != nil || len(devices) == 0 {
		return gin.H{"schedule_id": scheduleID, "total": 0, "acked": 0, "failed": 0, "pending": 0, "available": false}
	}
	// Per-device state lives in gateway_db; central reads it through the internal
	// API rather than opening a second connection to a schema it does not own.
	counts := map[string]int64{"acked": 0, "failed": 0, "pending": 0}
	for _, deviceID := range devices {
		status, ok := a.deviceOtaStatus(ctx, deviceID)
		if !ok {
			counts["pending"]++
			continue
		}
		counts[status]++
	}
	return gin.H{"schedule_id": scheduleID, "total": len(devices), "acked": counts["acked"],
		"failed": counts["failed"], "pending": counts["pending"], "available": true}
}

// deviceOtaStatus returns the latest command status for one device.
func (a OtaAPI) deviceOtaStatus(ctx context.Context, deviceID string) (string, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(a.GatewayURL, "/")+"/api/v1/internal/ota/status?device_id="+url.QueryEscape(deviceID), nil)
	if err != nil {
		return "", false
	}
	request.Header.Set("X-Service-Token", a.ServiceToken)
	response, err := (&http.Client{Timeout: 8 * time.Second}).Do(request)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != http.StatusOK {
		return "", false
	}
	var envelope struct {
		Data struct {
			Commands []struct {
				Status string `json:"status"`
			} `json:"commands"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil || len(envelope.Data.Commands) == 0 {
		return "pending", true
	}
	return envelope.Data.Commands[0].Status, true
}

// targetDevicesByCode resolves the rollout filter against gateway devices, which
// is where the authoritative active-device list lives.
func (a OtaAPI) targetDevicesByCode(ctx context.Context, raw *string, vendorID *uint64) ([]string, error) {
	query := a.Store.AdminDB.WithContext(ctx).Table("device_meta").Where("deleted_at IS NULL")
	if vendorID != nil {
		query = query.Where("vendor_id = ?", *vendorID)
	}
	if raw != nil && *raw != "" && *raw != "null" {
		var filter struct {
			StationIDs []uint64 `json:"station_ids"`
		}
		if err := json.Unmarshal([]byte(*raw), &filter); err == nil && len(filter.StationIDs) > 0 {
			query = query.Where("station_id IN ?", filter.StationIDs)
		}
	}
	var rows []struct {
		DeviceID string `gorm:"column:device_id"`
	}
	if err := query.Select("device_id").Order("device_id").Limit(2000).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.DeviceID != "" && len(row.DeviceID) <= 64 {
			out = append(out, row.DeviceID)
		}
	}
	return out, nil
}

// triggerSchedule pushes the first batch to gateway. Batches advance on explicit
// operator triggers so a failed rollout can be halted before the next wave.
func (a OtaAPI) triggerSchedule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var schedule struct {
		ID              uint64
		PackageID       uint64  `gorm:"column:package_id"`
		RolloutStrategy string  `gorm:"column:rollout_strategy"`
		BatchSize       *uint32 `gorm:"column:batch_size"`
		Status          string  `gorm:"column:status"`
		TargetJSON      *string `gorm:"column:target_filter_json"`
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("ota_schedule").Where("id = ?", id).Take(&schedule).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if schedule.Status != "pending" && schedule.Status != "running" {
		httpapi.Write(c, 409, 2009, "当前计划状态不允许触发", nil)
		return
	}
	var pkg struct {
		Code     string  `gorm:"column:code"`
		Version  string  `gorm:"column:version"`
		URL      string  `gorm:"column:storage_url"`
		Checksum string  `gorm:"column:checksum_sha256"`
		Status   string  `gorm:"column:status"`
		VendorID *uint64 `gorm:"column:vendor_id"`
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("ota_package").Where("id = ? AND deleted_at IS NULL", schedule.PackageID).Take(&pkg).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if pkg.Status != "published" {
		httpapi.Write(c, 409, 2009, "固件包未发布或已归档", nil)
		return
	}
	devices, err := a.targetDevices(c, schedule.TargetJSON, pkg.VendorID)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	batch := devices
	limit := 0
	if schedule.RolloutStrategy == "canary" && schedule.BatchSize != nil {
		limit = int(*schedule.BatchSize)
	}
	if limit > 0 && len(batch) > limit {
		batch = batch[:limit]
	}
	if len(batch) == 0 {
		httpapi.Write(c, 409, 2009, "没有匹配的目标设备", nil)
		return
	}
	if err := a.pushToGateway(c, schedule.ID, pkg, batch); err != nil {
		resourceFailure(c, err)
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		values := map[string]any{"status": "running"}
		if schedule.Status == "pending" {
			values["started_at"] = time.Now().UTC()
		}
		if err := tx.Table("ota_schedule").Where("id = ? AND status = ?", id, schedule.Status).Updates(values).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "trigger", "ota_schedule", id, nil, gin.H{"batch_size": len(batch)}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "dispatched": len(batch)})
}

// targetDevices resolves the rollout filter against provisioned, active devices.
func (a OtaAPI) targetDevices(c *gin.Context, raw *string, vendorID *uint64) ([]string, error) {
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("device_meta").Where("deleted_at IS NULL")
	if vendorID != nil {
		query = query.Where("vendor_id = ?", *vendorID)
	}
	if raw != nil && *raw != "" && *raw != "null" {
		var filter struct {
			StationIDs []uint64 `json:"station_ids"`
		}
		if err := json.Unmarshal([]byte(*raw), &filter); err == nil && len(filter.StationIDs) > 0 {
			query = query.Where("station_id IN ?", filter.StationIDs)
		}
	}
	var rows []struct {
		DeviceID string `gorm:"column:device_id"`
	}
	if err := query.Select("device_id").Order("device_id").Limit(2000).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.DeviceID != "" && len(row.DeviceID) <= 64 {
			out = append(out, row.DeviceID)
		}
	}
	return out, nil
}

func (a OtaAPI) pushToGateway(c *gin.Context, scheduleID uint64, pkg struct {
	Code     string  `gorm:"column:code"`
	Version  string  `gorm:"column:version"`
	URL      string  `gorm:"column:storage_url"`
	Checksum string  `gorm:"column:checksum_sha256"`
	Status   string  `gorm:"column:status"`
	VendorID *uint64 `gorm:"column:vendor_id"`
}, devices []string) error {
	body, err := json.Marshal(map[string]any{
		"schedule_id": scheduleID, "package_id": pkg.Code, "package_version": pkg.Version,
		"checksum_sha256": pkg.Checksum, "storage_url": pkg.URL, "device_ids": devices,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, strings.TrimSuffix(a.GatewayURL, "/")+"/api/v1/internal/ota/dispatch", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", a.ServiceToken)
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("gateway rejected the OTA dispatch: %s", strings.TrimSpace(string(payload)))
	}
	return nil
}

func (a OtaAPI) cancelSchedule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row struct {
			Status string `gorm:"column:status"`
		}
		if err := tx.Table("ota_schedule").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		if row.Status != "pending" && row.Status != "running" {
			return errConflict
		}
		if err := tx.Table("ota_schedule").Where("id = ? AND status IN ('pending','running')", id).
			Updates(map[string]any{"status": "cancelled", "completed_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "cancel", "ota_schedule", id, nil, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": "cancelled"})
}
