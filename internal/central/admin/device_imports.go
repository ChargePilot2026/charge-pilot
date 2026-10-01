package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ImportDevice 描述批量导入的设备身份、厂商、站点、端口数、型号及计费模式。
// 设备计量能力由所选协议确定。
type ImportDevice struct {
	ProtocolAdapter string  `json:"protocol_adapter"` // Resolved by the server from the selected vendor; never trust the client.
	DeviceID        string  `json:"device_id"`        // 设备编号，全局唯一，8–32 位字母数字及 _ -，大小写不敏感判重
	VendorID        uint64  `json:"vendor_id"`        // 厂商 ID，必须非 0
	StationID       uint64  `json:"station_id"`       // 所属站点 ID，必须非 0 且该站点处于 active
	PortCount       uint8   `json:"port_count"`       // 充电端口数，必须非 0
	Model           *string `json:"model"`            // 型号，可空；非空时最多 128 字
	// 兼容导入字段；实际计量能力按协议计算，不依赖客户端人工声明。
	ChargeMode            string `json:"charge_mode"`             // 计费方式，取值为计费引擎认可的六种模式之一；留空表示不覆盖
	ReportsEnergy         bool   `json:"reports_energy"`          // 是否上报电量，引擎据此决定该设备能否用带电表的计费方式
	ReportsSegmentedPower bool   `json:"reports_segmented_power"` // 是否上报分段功率，同上
}

// ImportJob 是一次批量导入任务在 device_import 表中的状态投影，也是导入列表接口的返回行。
// 原始请求体只存库不外发（json："-"），列表页因此看不到设备明细，只能看任务进展。
type ImportJob struct {
	ImportID    string  `json:"import_id"`  // 导入任务号，客户端生成的幂等键
	Status      string  `json:"status"`     // 任务状态：pending 待执行 / completed 已完成 / failed 失败可重试
	LastError   *string `json:"last_error"` // 最近一次失败原因，成功时为 nil
	RequestJSON string  `json:"-"`          // 原始设备清单 JSON，重试时据此重放，不返回给前端
	ActorID     uint64  `json:"actor_id"`   // 发起导入的管理员 ID
	Attempts    uint32  `json:"attempts"`   // 已执行次数，每次调用网关后 +1
}

// registerImports 挂载设备批量导入的三个路由：任务列表、提交导入、重试。
// 三者共用 device.import 权限——导入本身就是开通设备的动作，查看列表也一并受它约束。
func (a ResourceAPI) registerImports(r *gin.Engine) {
	r.GET("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.imports)
	r.POST("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.createImport)
	r.POST("/api/v1/admin/device-imports/:import_id/retry", a.Auth.Require("device.import"), a.retryImport)
}

// imports 返回最近的导入任务（最多 100 条），供运营在看板上确认某批设备到底导进去没有。
func (a ResourceAPI) imports(c *gin.Context) {
	rows := []ImportJob{}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("device_import").Order("created_at DESC").Limit(100).Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, rows)
}

func requireImportScope(c *gin.Context, scope DataScope, devices []ImportDevice) bool {
	for _, device := range devices {
		if !scope.AllowsStation(device.StationID) || !scope.AllowsVendor(device.VendorID) {
			httpapi.Write(c, 403, 1003, "导入设备的站点或厂商不在您的数据范围内", nil)
			return false
		}
	}
	return true
}

// createImport 校验 1–100 台设备的编号、归属、端口和计费模式。
// 事务内校验请求及设备身份幂等性，仅对新增设备执行能力检查；保存 pending 任务后执行导入。
func (a ResourceAPI) createImport(c *gin.Context) {
	var in struct {
		ImportID string         `json:"import_id"` // 导入任务号，UUID，作为幂等键
		Devices  []ImportDevice `json:"devices"`   // 设备清单，1–100 台，批内编号不得重复
	}
	if !decodeResource(c, &in) {
		return
	}
	if !requestIDPattern.MatchString(in.ImportID) || len(in.Devices) == 0 || len(in.Devices) > 100 {
		httpapi.BadRequest(c, "导入编号无效或设备数不在 1–100 范围")
		return
	}
	seen := map[string]bool{}
	for _, d := range in.Devices {
		key := strings.ToLower(d.DeviceID)
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{8,32}$`).MatchString(d.DeviceID) || d.PortCount == 0 || d.VendorID == 0 || d.StationID == 0 || seen[key] || (d.Model != nil && utf8.RuneCountInString(*d.Model) > 128) {
			httpapi.BadRequest(c, "设备编号、端口数、厂商或站点无效/重复")
			return
		}
		if d.ChargeMode != "" && !validDeviceChargeMode(d.ChargeMode) {
			// 提交任务前校验计费模式，避免保存无法执行的导入。
			httpapi.BadRequest(c, "设备 "+d.DeviceID+" 的计费方式无效")
			return
		}
		seen[key] = true
	}
	scope, ok := a.stationScope(c)
	if !ok || !requireImportScope(c, scope, in.Devices) {
		return
	}
	// Resolve once per vendor; this trusted snapshot also survives import retries.
	adapters := map[uint64]string{}
	for i := range in.Devices {
		d := &in.Devices[i]
		adapter, found := adapters[d.VendorID]
		if !found {
			var vendor vendorRow
			if err := a.requestVendor(c.Request.Context(), http.MethodGet, "/"+strconv.FormatUint(d.VendorID, 10), nil, nil, &vendor, httpapi.RequestID(c)); err != nil {
				vendorFailure(c, err)
				return
			}
			if vendor.Status != "enabled" || vendor.Protocol != "tcp" {
				httpapi.BadRequest(c, "请选择已启用且支持的通信协议厂商")
				return
			}
			if _, err := pricing.ProtocolCapabilities(vendor.AdapterClass); err != nil {
				httpapi.BadRequest(c, err.Error())
				return
			}
			adapter = vendor.AdapterClass
			adapters[d.VendorID] = adapter
		}
		d.ProtocolAdapter = adapter
		cap, _ := pricing.ProtocolCapabilities(adapter)
		d.ReportsEnergy, d.ReportsSegmentedPower = cap.ReportsEnergy, cap.ReportsSegmentedPower
	}
	body, _ := json.Marshal(in.Devices)
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var prior ImportJob
		err := tx.Table("device_import").Clauses(clause.Locking{Strength: "UPDATE"}).Where("import_id=?", in.ImportID).Take(&prior).Error
		if err == nil {
			var old []ImportDevice
			if json.Unmarshal([]byte(prior.RequestJSON), &old) != nil {
				return errConflict
			}
			b, _ := json.Marshal(old)
			if string(b) != string(body) || prior.ActorID != p.ID {
				return errConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		for _, d := range in.Devices {
			var station Station
			if err := tx.Where("id=? AND deleted_at IS NULL AND status='active'", d.StationID).Take(&station).Error; err != nil {
				return err
			}
			b, _ := json.Marshal(d)
			if err := tx.Exec("INSERT IGNORE INTO device_import_identity(device_id,request_json) VALUES (?,?)", d.DeviceID, string(b)).Error; err != nil {
				return err
			}
			var identity struct{ RequestJSON string } // 该设备编号首次登记时的内容快照，用来拒绝同一编号被改成不同内容
			if err := tx.Table("device_import_identity").Clauses(clause.Locking{Strength: "UPDATE"}).Where("device_id=?", d.DeviceID).Take(&identity).Error; err != nil {
				return err
			}
			var old ImportDevice
			_ = json.Unmarshal([]byte(identity.RequestJSON), &old)
			oldJSON, _ := json.Marshal(old)
			if string(oldJSON) != string(b) {
				return errConflict
			}
		}
		// 仅新增设备需要能力校验；已归属本站点的设备保持已有配置。
		arriving, err := newDevicesOnly(tx, in.Devices)
		if err != nil {
			return err
		}
		// 整批设备统一校验，任一设备不兼容时拒绝整批导入。
		if err := checkImportAgainstStation(tx, arriving); err != nil {
			var blocked *errMeteringBlocked
			if errors.As(err, &blocked) {
				httpapi.Write(c, 409, 1009, err.Error()+"，请检查所选通信协议或站点计费方式", nil)
				return errAlreadyReported
			}
			return err
		}
		if err := tx.Table("device_import").Create(map[string]any{"import_id": in.ImportID, "actor_id": p.ID, "request_json": string(body), "status": "pending"}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "create", "device_import", 0, nil, in, c.ClientIP(), in.ImportID)
	})
	// 校验失败已写入响应，立即返回，避免追加第二份 JSON 响应。
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.runImport(c, in.ImportID)
}

// retryImport 重试一笔失败的导入任务：只校验任务号格式，然后按存档的设备清单重新走一遍执行流程。
// 是否可重试由 runImport 依据任务状态判断，这里不做额外判断。
func (a ResourceAPI) retryImport(c *gin.Context) {
	if !requestIDPattern.MatchString(c.Param("import_id")) {
		httpapi.BadRequest(c, "导入编号无效")
		return
	}
	a.runImport(c, c.Param("import_id"))
}

// runImport 调用网关开通接口后补建尚不存在的设备元数据；请求超时为 10 秒。
// 已有设备不覆盖配置，厂商或站点不一致时拒绝。失败任务记录原因并递增 attempts；已完成任务保持幂等。
func (a ResourceAPI) runImport(c *gin.Context, id string) {
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	var result ImportJob
	// 在这次有界的内部请求期间一直持着任务锁。崩溃会把元数据一起回滚，
	// 重放则能安全地看到网关那边已经持久占用的设备身份。
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("device_import").Clauses(clause.Locking{Strength: "UPDATE"}).Where("import_id=?", id).Take(&result).Error; err != nil {
			return err
		}
		var devices []ImportDevice
		if err := json.Unmarshal([]byte(result.RequestJSON), &devices); err != nil {
			return err
		}
		if !requireImportScope(c, scope, devices) {
			return errAlreadyReported
		}
		if result.Status == "completed" {
			return nil
		}
		body, _ := json.Marshal(gin.H{"devices": devices})
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.GatewayURL, "/")+"/api/v1/internal/devices/provision", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Service-Token", a.ServiceToken)
			var res *http.Response
			res, err = (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err == nil {
				defer res.Body.Close()
				_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
				if res.StatusCode != 200 {
					err = fmt.Errorf("gateway HTTP %d", res.StatusCode)
				}
			}
		}
		if err != nil {
			message := "网关未确认导入，请核对厂商与设备参数后重试；dc589 最多 20 路"
			result.Status = "failed"
			result.LastError = &message
			return tx.Table("device_import").Where("import_id=?", id).Updates(map[string]any{"status": "failed", "last_error": message, "attempts": gorm.Expr("attempts+1")}).Error
		}
		for _, d := range devices {
			var existing struct {
				ID, VendorID, StationID uint64  // 已登记设备的主键、厂商与站点；与导入内容对不上即判冲突
				Model                   *string // 已登记的型号，重新导入时不覆盖
			}
			e := tx.Table("device_meta").Clauses(clause.Locking{Strength: "UPDATE"}).Where("device_id=?", d.DeviceID).Take(&existing).Error
			if e == nil {
				if existing.VendorID != d.VendorID || existing.StationID != d.StationID {
					return errConflict
				}
				continue
			}
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			row := map[string]any{
				"device_id": d.DeviceID, "vendor_id": d.VendorID, "station_id": d.StationID,
				"model": d.Model, "status": "enabled",
				"protocol_adapter": d.ProtocolAdapter,
			}
			// 已存在的设备保持原配置；重复导入不得以缺省字段覆盖其能力或计费设置。
			if d.ChargeMode != "" {
				row["charge_mode"] = d.ChargeMode
			}
			if err := tx.Table("device_meta").Create(row).Error; err != nil {
				return err
			}
		}
		result.Status = "completed"
		result.LastError = nil
		if err := tx.Table("device_import").Where("import_id=?", id).Updates(map[string]any{"status": "completed", "last_error": nil, "attempts": gorm.Expr("attempts+1"), "retryable": false}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "complete", "device_import", 0, nil, gin.H{"import_id": id, "count": len(devices)}, c.ClientIP(), id)
	})
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if result.Status == "failed" {
		httpapi.Write(c, 503, 5003, *result.LastError, result)
		return
	}
	httpapi.OK(c, result)
}
