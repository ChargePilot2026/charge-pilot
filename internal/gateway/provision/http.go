package provision

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Device struct {
	DeviceID  string  `json:"device_id"`
	VendorID  uint64  `json:"vendor_id"`
	StationID uint64  `json:"station_id"`
	PortCount uint8   `json:"port_count"`
	Model     *string `json:"model"`
}
type API struct {
	DB           *gorm.DB
	ServiceToken string
}

func (a API) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/devices/provision", a.provision)
	a.registerVendors(r)
}

var errConflict = errors.New("device provisioning conflict")

func (a API) provision(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, 401, 1001, "service token invalid", nil)
		return
	}
	var in struct {
		Devices []Device `json:"devices"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if c.ShouldBindJSON(&in) != nil || len(in.Devices) == 0 || len(in.Devices) > 100 {
		httpapi.BadRequest(c, "invalid device batch")
		return
	}
	seen := map[string]bool{}
	for _, d := range in.Devices {
		k := strings.ToLower(d.DeviceID)
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{8,32}$`).MatchString(d.DeviceID) || d.StationID == 0 || d.VendorID == 0 || d.PortCount == 0 || seen[k] || (d.Model != nil && utf8.RuneCountInString(*d.Model) > 128) {
			httpapi.BadRequest(c, "invalid device parameters")
			return
		}
		seen[k] = true
	}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, d := range in.Devices {
			var vendor struct {
				Status       string
				AdapterClass string
			}
			// Serialize vendor edits with provisioning, so protocol changes cannot
			// pass their device check while this transaction is creating a device.
			if err := tx.Table("vendor").Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND deleted_at IS NULL", d.VendorID).Take(&vendor).Error; err != nil {
				return err
			}
			if vendor.Status != "enabled" {
				return errConflict
			}
			if vendor.AdapterClass == "dc589" && d.PortCount > 20 {
				return errConflict
			}
			b, _ := json.Marshal(d)
			if err := tx.Exec("INSERT IGNORE INTO device_provision(device_id,request_json) VALUES (?,?)", d.DeviceID, string(b)).Error; err != nil {
				return err
			}
			var identity struct{ RequestJSON string }
			if err := tx.Table("device_provision").Clauses(clause.Locking{Strength: "UPDATE"}).Where("device_id=?", d.DeviceID).Take(&identity).Error; err != nil {
				return err
			}
			var existing Device
			if json.Unmarshal([]byte(identity.RequestJSON), &existing) != nil {
				return errConflict
			}
			old, _ := json.Marshal(existing)
			if string(old) != string(b) {
				return errConflict
			}
			var device struct {
				ID, VendorID, StationID uint64
				PortCount               uint8
				Model                   *string
			}
			e := tx.Table("device").Where("device_id=?", d.DeviceID).Take(&device).Error
			if e == nil {
				if device.VendorID != d.VendorID || device.StationID != d.StationID || device.PortCount != d.PortCount {
					return errConflict
				}
				continue
			}
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			if err := tx.Table("device").Create(map[string]any{"device_id": d.DeviceID, "vendor_id": d.VendorID, "station_id": d.StationID, "port_count": d.PortCount, "model": d.Model, "status": "enabled"}).Error; err != nil {
				return err
			}
			for port := 1; port <= int(d.PortCount); port++ {
				if err := tx.Table("device_port").Create(map[string]any{"device_id": d.DeviceID, "port_no": port, "port_code": fmt.Sprintf("%s-%d", d.DeviceID, port), "status": "idle"}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, 409, 2009, "设备已存在且参数不同，或厂商无效（dc589 最多 20 路）", nil)
		} else {
			httpapi.Write(c, 503, 5003, "设备配置暂时不可用", nil)
		}
		return
	}
	httpapi.OK(c, gin.H{"provisioned": len(in.Devices)})
}
