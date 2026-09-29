package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ImportDevice struct {
	DeviceID  string  `json:"device_id"`
	VendorID  uint64  `json:"vendor_id"`
	StationID uint64  `json:"station_id"`
	PortCount uint8   `json:"port_count"`
	Model     *string `json:"model"`
	// The metering capability is carried on the import so a fleet can be
	// classified in one go, which is the only practical way to backfill it:
	// a device that was never classified is refused every tariff that needs a
	// meter, so leaving these unset means leaving the whole fleet unpriceable.
	ChargeMode            string `json:"charge_mode"`
	ReportsEnergy         bool   `json:"reports_energy"`
	ReportsSegmentedPower bool   `json:"reports_segmented_power"`
}
type ImportJob struct {
	ImportID    string  `json:"import_id"`
	Status      string  `json:"status"`
	LastError   *string `json:"last_error"`
	RequestJSON string  `json:"-"`
	ActorID     uint64  `json:"actor_id"`
	Attempts    uint32  `json:"attempts"`
}

func (a ResourceAPI) registerImports(r *gin.Engine) {
	r.GET("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.imports)
	r.POST("/api/v1/admin/device-imports", a.Auth.Require("device.import"), a.createImport)
	r.POST("/api/v1/admin/device-imports/:import_id/retry", a.Auth.Require("device.import"), a.retryImport)
}
func (a ResourceAPI) imports(c *gin.Context) {
	rows := []ImportJob{}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("device_import").Order("created_at DESC").Limit(100).Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, rows)
}
func (a ResourceAPI) createImport(c *gin.Context) {
	var in struct {
		ImportID string         `json:"import_id"`
		Devices  []ImportDevice `json:"devices"`
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
			// Checked here rather than at apply time. A fleet is imported once;
			// finding out months later that one row carries a mode the engine
			// cannot price would be a very expensive way to learn it.
			httpapi.BadRequest(c, "设备 "+d.DeviceID+" 的计费方式无效")
			return
		}
		seen[key] = true
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
			var identity struct{ RequestJSON string }
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
		// Only boards that are actually new are checked. A board already in this
		// yard is running whatever the yard was charging when it arrived, and
		// refusing to re-import it would block a routine correction for a
		// condition that was true before the re-import and is unchanged by it.
		arriving, err := newDevicesOnly(tx, in.Devices)
		if err != nil {
			return err
		}
		// Checked on the batch, so the whole import is refused rather than part
		// of it: a fleet that is half priceable and half not is a fleet an
		// operator has to reconcile by hand before anything can be billed.
		if err := checkImportAgainstYard(tx, arriving); err != nil {
			var blocked *errMeteringBlocked
			if errors.As(err, &blocked) {
				httpapi.Write(c, 409, 1009, err.Error()+"，请补录该设备的计量能力或先改用该场地可执行的计费方式", nil)
				return errAlreadyReported
			}
			return err
		}
		if err := tx.Table("device_import").Create(map[string]any{"import_id": in.ImportID, "actor_id": p.ID, "request_json": string(body), "status": "pending"}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "create", "device_import", 0, nil, in, c.ClientIP(), in.ImportID)
	})
	// The handler above has already written the refusal, so the generic failure
	// path must not write a second body on top of it. Two JSON documents in one
	// response is not a response any client can read, and the operator sees a
	// generic database error instead of the reason their fleet was turned away.
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.runImport(c, in.ImportID)
}
func (a ResourceAPI) retryImport(c *gin.Context) {
	if !requestIDPattern.MatchString(c.Param("import_id")) {
		httpapi.BadRequest(c, "导入编号无效")
		return
	}
	a.runImport(c, c.Param("import_id"))
}
func (a ResourceAPI) runImport(c *gin.Context, id string) {
	var result ImportJob
	// Hold the job lock across the bounded internal request. A crash rolls back
	// metadata; replay safely observes the gateway's durable identity reservation.
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("device_import").Clauses(clause.Locking{Strength: "UPDATE"}).Where("import_id=?", id).Take(&result).Error; err != nil {
			return err
		}
		if result.Status == "completed" {
			return nil
		}
		var devices []ImportDevice
		if err := json.Unmarshal([]byte(result.RequestJSON), &devices); err != nil {
			return err
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
				ID, VendorID, StationID uint64
				Model                   *string
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
				"reports_energy": d.ReportsEnergy, "reports_segmented_power": d.ReportsSegmentedPower,
			}
			// An existing device keeps whatever it was already classified as.
			// The import is a fleet onboarding record, not a re-classification;
			// overwriting a declared capability because a re-import omitted the
			// field would un-price every board in the yard at a stroke.
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
