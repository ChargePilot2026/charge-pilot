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

// OtaAPI 管理固件包与推送计划：admin_db 拥有推送计划和固件包元数据；
// 设备侧的下发与回执住在 gateway_db，
// 通过带服务令牌的内部 API 驱动。
// OtaAPI 管理固件包与推送计划：计划与包元数据在管理库，真正下发和回执在网关库，
// 所以这里通过 GatewayURL + ServiceToken 调网关的内部接口，中台不去直连网关库。
type OtaAPI struct {
	Store                    ResourceStore // 数据库连接集合,固件包与推送计划都读写管理库
	Auth                     API           // 鉴权器,OTA 的读、改包、下发、取消各有独立权限
	GatewayURL, ServiceToken string        // GatewayURL 是网关服务地址;ServiceToken 是调网关内部接口的服务令牌
}

// otaPackage 是一个固件包（管理库 ota_package 表），只保存固件文件的元数据与下载地址；
// 文件本体必须在外部 HTTPS 存储里，中台不托管二进制。
type otaPackage struct {
	ID             uint64  `json:"id"`              // 固件包主键 ID
	Code           string  `json:"code"`            // 厂商固件编码,同一编码下用 version 区分不同版本
	VendorID       *uint64 `json:"vendor_id"`       // 厂商 ID(可空),为空表示该固件不限厂商
	Version        string  `json:"version"`         // 版本号,允许字母数字与 . _ -
	StorageURL     string  `json:"storage_url"`     // 固件文件 HTTPS 下载地址
	SizeBytes      uint64  `json:"size_bytes"`      // 固件大小,单位字节,上限 2GiB
	ChecksumSHA256 string  `json:"checksum_sha256"` // 文件 sha256 校验和,小写 64 位十六进制
	Sign           *string `json:"sign"`            // 厂商固件签名(可空),最长 512 字符
	ReleaseNotes   *string `json:"release_notes"`   // 更新说明(可空),最长 20000 字符
	Status         string  `json:"status"`          // 包状态:draft 草稿 / published 已发布(可建推送计划) / archived 已归档
	CreatedAt      string  `json:"created_at"`      // 创建时间
}

// otaSchedule 是一条固件推送计划（管理库 ota_schedule 表），描述“把哪个包、按什么策略、推给哪些设备”。
// 实际下发由网关执行，中台只维护计划状态并展示进度。
type otaSchedule struct {
	ID        uint64 `json:"id"`         // 推送计划主键 ID
	PackageID uint64 `json:"package_id"` // 固件包 ID,必须指向 published 状态的包
	// TargetFilter 是查询完之后才解码的；库里那一列存的是 JSON。
	TargetFilter    map[string]any `json:"target_filter" gorm:"-"` // 目标设备筛选条件,查询后从 target_filter_json 解析回填,目前识别 station_ids
	RolloutStrategy string         `json:"rollout_strategy"`       // 发布策略:all 全量 / canary 灰度 / batch 分批 / manual 手动
	BatchSize       *uint32        `json:"batch_size"`             // canary 策略的每批设备数(可空),取值 1-10000
	Status          string         `json:"status"`                 // 计划状态:pending 待触发 / running 进行中 / completed 已完成 / cancelled 已取消 / failed 失败
	ScheduledAt     *string        `json:"scheduled_at"`           // 计划执行时间(可空),为空表示不定时,完全靠人工触发
	StartedAt       *string        `json:"started_at"`             // 首次触发时间(可空)
	CompletedAt     *string        `json:"completed_at"`           // 完成或取消时间(可空)
	Progress        map[string]any `json:"progress" gorm:"-"`      // 下发进度统计(总数/已回执/失败/待回执),查询时逐台向网关查得
}

// registerOta 注册 OTA 固件包与推送计划路由；读、改包、下发、取消各自独立授权。
func (a OtaAPI) registerOta(r *gin.Engine) {
	r.GET("/api/v1/admin/ota/packages", a.Auth.Require("ota.read"), a.listPackages)
	r.POST("/api/v1/admin/ota/packages", a.Auth.Require("ota.package.create"), a.createPackage)
	r.DELETE("/api/v1/admin/ota/packages/:id", a.Auth.Require("ota.package.delete"), a.deletePackage)
	r.POST("/api/v1/admin/ota/schedules", a.Auth.Require("ota.schedule.create"), a.createSchedule)
	r.GET("/api/v1/admin/ota/schedules", a.Auth.Require("ota.read"), a.listSchedules)
	r.POST("/api/v1/admin/ota/schedules/:id/trigger", a.Auth.Require("ota.schedule.trigger"), a.triggerSchedule)
	r.POST("/api/v1/admin/ota/schedules/:id/cancel", a.Auth.Require("ota.schedule.trigger"), a.cancelSchedule)
}

// firmwareVersionPattern 限定版本号格式，避免任意字符串进入后续拼 URL 的下发链路。
var firmwareVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// listPackages 分页返回固件包，支持按状态和关键字（编码或版本号）过滤，按 ID 倒序。
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

// createPackage 登记固件元数据。固件本体必须已经放在 HTTPS 存储里，
// 这里只接受一个能用校验和验证的引用。
// createPackage 登记一个固件包：校验编码、版本、HTTPS 地址、大小上限与 sha256 格式后写入，
// publish=true 直接置为 published，否则存为草稿；厂商存在性与审计流水在同一事务里完成。
func (a OtaAPI) createPackage(c *gin.Context) {
	var in struct {
		Code           string  `json:"code"`            // 厂商固件编码,最长 64 字符
		VendorID       *uint64 `json:"vendor_id"`       // 厂商 ID(可空);传了就必须指向未删除的厂商
		Version        string  `json:"version"`         // 版本号,需匹配版本格式
		StorageURL     string  `json:"storage_url"`     // 固件下载地址,必须是 HTTPS
		SizeBytes      uint64  `json:"size_bytes"`      // 固件大小,单位字节,不能为 0 且不超过 2GiB
		ChecksumSHA256 string  `json:"checksum_sha256"` // sha256 校验和,64 位十六进制,入库时转小写
		Sign           *string `json:"sign"`            // 固件签名(可空),最长 512 字符
		ReleaseNotes   *string `json:"release_notes"`   // 更新说明(可空),最长 20000 字符
		Publish        bool    `json:"publish"`         // 是否立即发布;为 false 时存为草稿,草稿不能建推送计划
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

// verifyChecksumFormat 校验固件校验和是不是 64 位十六进制 sha256(比较时忽略大小写)。
func verifyChecksumFormat(value string) error {
	decoded, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(value)))
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("固件校验和必须是 64 位十六进制 sha256")
	}
	return nil
}

// deletePackage 归档并软删固件包：有 running 状态的推送计划时拒绝删除，
// 否则设备会被留在一个后端已不再提供的固件上；删除前留快照并写审计流水。
func (a OtaAPI) deletePackage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var running int64
		// 推送进行到一半的固件包不能下架，否则设备会指向
		// 一个后端已经不再提供的固件。
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

// createSchedule 新建推送计划：固件包必须是 published，策略限定 all/canary/batch/manual，
// canary 必须给出 1-10000 的批次数量，目标筛选条件存 JSON；计划初始为 pending，不会自动下发。
func (a OtaAPI) createSchedule(c *gin.Context) {
	var in struct {
		PackageID       uint64         `json:"package_id"`       // 固件包 ID,必须存在、未删除且状态为 published
		TargetFilter    map[string]any `json:"target_filter"`    // 目标设备筛选条件,如 {"station_ids":[1,2]};为空表示全部设备
		RolloutStrategy string         `json:"rollout_strategy"` // 发布策略:all / canary / batch / manual
		BatchSize       *uint32        `json:"batch_size"`       // canary 每批设备数(可空),取值 1-10000
		ScheduledAt     *time.Time     `json:"scheduled_at"`     // 计划执行时间(可空),格式 RFC3339
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
			Status string // 固件包状态,只有 published 能建推送计划
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

// listSchedules 分页返回推送计划并支持按状态过滤；进度要逐台问网关，目标筛选 JSON 读回后再解析。
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
			// 这一列是 JSON，所以按文本读出来，再显式解码。
			_ = json.Unmarshal(raw, &out.Items[i].TargetFilter)
		}
	}
	httpapi.OK(c, out)
}

// scheduleProgress 报告有多少台设备回执了这次推送：数据经网关的内部状态 API 读，
// 不是从 admin_db 里读。
// scheduleProgress 汇总一次推送的进度：先按目标筛选算出设备清单，再逐台问网关最新指令状态；
// 网关不可用或没有目标设备时返回 available=false，让前端显示“进度未知”而不是“全部待回执”。
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
	// 逐台设备的状态住在 gateway_db；中台通过内部 API 读它，
	// 而不是给自己并不拥有的 schema 再开第二条连接。
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

// deviceOtaStatus 返回某台设备最近一条指令的状态。
// deviceOtaStatus 通过网关内部接口取某台设备最近一条 OTA 指令状态；
// 网关没有记录时按 pending 处理，请求失败或返回非 200 时返回 ok=false 交调用方兜底。
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

// targetDevicesByCode 拿网关的设备来解析推送筛选条件，
// 因为在用设备的权威清单就在网关那边。
// targetDevicesByCode 按厂商与站点筛选条件解析目标设备号列表（读管理库 device_meta），
// 最多返回 2000 台，设备号非空且不超过 64 字符才保留，结果按设备号升序。
func (a OtaAPI) targetDevicesByCode(ctx context.Context, raw *string, vendorID *uint64) ([]string, error) {
	query := a.Store.AdminDB.WithContext(ctx).Table("device_meta").Where("deleted_at IS NULL")
	if vendorID != nil {
		query = query.Where("vendor_id = ?", *vendorID)
	}
	if raw != nil && *raw != "" && *raw != "null" {
		var filter struct {
			StationIDs []uint64 `json:"station_ids"` // 目标筛选条件里解析出的站点 ID 列表,非空时只推这些站点
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

// triggerSchedule 把第一批推给网关。每一批都靠运营显式触发才往下走，
// 这样一次失败的推送能在下一波开始之前被叫停。
// triggerSchedule 手动触发一批下发：只有 pending/running 计划可触发，固件包必须仍是 published，
// 解析目标设备后按 canary 批次量截取再推给网关；成功后计划置为 running 并首次写入开始时间。
func (a OtaAPI) triggerSchedule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var schedule struct {
		ID              uint64  // 计划 ID
		PackageID       uint64  `gorm:"column:package_id"`         // 固件包 ID
		RolloutStrategy string  `gorm:"column:rollout_strategy"`   // 发布策略:all/canary/batch/manual
		BatchSize       *uint32 `gorm:"column:batch_size"`         // canary 批次数量(可空)
		Status          string  `gorm:"column:status"`             // 当前状态,只有 pending 或 running 允许继续触发
		TargetJSON      *string `gorm:"column:target_filter_json"` // 目标筛选条件 JSON 原文(可空)
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
		Code     string  `gorm:"column:code"`            // 固件编码
		Version  string  `gorm:"column:version"`         // 固件版本号
		URL      string  `gorm:"column:storage_url"`     // 固件下载地址
		Checksum string  `gorm:"column:checksum_sha256"` // sha256 校验和
		Status   string  `gorm:"column:status"`          // 包状态,下发时必须为 published
		VendorID *uint64 `gorm:"column:vendor_id"`       // 厂商 ID(可空),用于把下发范围限制在该厂商的设备上
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

// targetDevices 针对已开通的在用设备解析推送筛选条件。
// targetDevices 是下发链路用的设备解析，与 targetDevicesByCode 同一套筛选规则：
// 按厂商与站点过滤，上限 2000 台，返回升序的设备号列表。
func (a OtaAPI) targetDevices(c *gin.Context, raw *string, vendorID *uint64) ([]string, error) {
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("device_meta").Where("deleted_at IS NULL")
	if vendorID != nil {
		query = query.Where("vendor_id = ?", *vendorID)
	}
	if raw != nil && *raw != "" && *raw != "null" {
		var filter struct {
			StationIDs []uint64 `json:"station_ids"` // 目标筛选条件里解析出的站点 ID 列表,非空时只推这些站点
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

// pushToGateway 把这一批设备连同固件信息 POST 给网关的内部下发接口，带服务令牌鉴权；
// 响应非 2xx 一律视为失败，并把网关返回的错误正文带回，便于排查。
func (a OtaAPI) pushToGateway(c *gin.Context, scheduleID uint64, pkg struct {
	Code     string  `gorm:"column:code"`            // 固件编码
	Version  string  `gorm:"column:version"`         // 固件版本号
	URL      string  `gorm:"column:storage_url"`     // 固件下载地址
	Checksum string  `gorm:"column:checksum_sha256"` // sha256 校验和
	Status   string  `gorm:"column:status"`          // 包状态(下发时应为 published)
	VendorID *uint64 `gorm:"column:vendor_id"`       // 厂商 ID(可空)
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

// cancelSchedule 取消推送计划：行锁内只允许 pending/running 转 cancelled 并写完成时间，
// 已经结束的计划不能再取消；取消同样写审计流水。
func (a OtaAPI) cancelSchedule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row struct {
			Status string `gorm:"column:status"` // 当前状态,只有 pending 或 running 能取消
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
