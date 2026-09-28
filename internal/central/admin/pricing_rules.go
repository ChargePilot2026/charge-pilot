package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pricingPublication struct {
	RequestID       string           `json:"request_id"`
	Name            string           `json:"name"`
	StationID       uint64           `json:"station_id"`
	ExpectedVersion uint32           `json:"expected_version"`
	Mode            string           `json:"mode"`
	Periods         []pricing.Period `json:"time_of_use"`
	ServiceKWh      int64            `json:"service_fee_cents_per_kwh"`
	ServiceMinute   int64            `json:"service_fee_cents_per_min"`
	Minimum         int64            `json:"min_charge_cents"`
}

func (a ResourceAPI) registerPricing(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/charge-rules", a.Auth.Require("pricing.read"), a.pricingRules)
	r.POST("/api/v1/admin/settings/charge-rules", a.Auth.Require("pricing.rule.create"), a.publishPricing)
	r.POST("/api/v1/admin/settings/charge-rules/:id/disable", a.Auth.Require("pricing.rule.update"), a.disablePricing)
}

func (a ResourceAPI) pricingRules(c *gin.Context) {
	rows := []map[string]any{}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_rule r").Select("r.id,r.name,r.station_id,s.name AS station_name,r.mode,r.time_of_use_json,r.service_fee_cents_per_kwh,r.service_fee_cents_per_min,r.min_charge_cents,r.version,r.status,r.effective_from,r.effective_to").Joins("LEFT JOIN station s ON s.id=r.station_id AND s.deleted_at IS NULL").Where("r.deleted_at IS NULL").Order("r.id DESC").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// Publishing keeps old financial inputs immutable. The station row serializes
// version allocation and activation; payment intents already own their snapshot.
func (a ResourceAPI) publishPricing(c *gin.Context) {
	var in pricingPublication
	if !decodeResource(c, &in) {
		return
	}
	if _, err := uuid.Parse(in.RequestID); err != nil || in.StationID == 0 || !validText(in.Name, 128) {
		httpapi.BadRequest(c, "请填写有效请求编号、站点和规则名称")
		return
	}
	rule := pricing.Rule{Mode: in.Mode, Periods: in.Periods, ServiceCentsPerKWh: in.ServiceKWh, ServiceCentsPerMinute: in.ServiceMinute, MinimumCents: in.Minimum}
	if pricing.ValidateRule(rule) != nil {
		httpapi.BadRequest(c, "计费规则无效：时段须完整覆盖一天且不能重叠，金额须为非负整数")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	payload, _ := json.Marshal(in)
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	var id uint64
	var version uint32
	replayed := false
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var station Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
			return err
		}
		var receipt struct {
			ActorID     uint64
			PayloadHash string
			RuleID      uint64
			Version     uint32
		}
		result := tx.Table("pricing_publication").Where("request_id=?", in.RequestID).Find(&receipt)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if receipt.ActorID != p.ID || receipt.PayloadHash != hash {
				return errConflict
			}
			id, version, replayed = receipt.RuleID, receipt.Version, true
			return nil
		}
		var latest struct{ Version uint32 }
		if err := tx.Table("pricing_rule").Select("COALESCE(MAX(version),0) AS version").Where("station_id=?", in.StationID).Scan(&latest).Error; err != nil {
			return err
		}
		if latest.Version != in.ExpectedVersion || latest.Version == ^uint32(0) {
			return errConflict
		}
		version = latest.Version + 1
		periods, _ := json.Marshal(in.Periods)
		if err := tx.Table("pricing_rule").Where("station_id=? AND status='active'", in.StationID).Update("status", "disabled").Error; err != nil {
			return err
		}
		row := map[string]any{"name": strings.TrimSpace(in.Name), "station_id": in.StationID, "mode": in.Mode, "time_of_use_json": string(periods), "service_fee_cents_per_kwh": in.ServiceKWh, "service_fee_cents_per_min": in.ServiceMinute, "min_charge_cents": in.Minimum, "version": version, "status": "active"}
		if err := tx.Table("pricing_rule").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		if err := tx.Table("pricing_publication").Create(map[string]any{"request_id": in.RequestID, "actor_id": p.ID, "payload_hash": hash, "rule_id": id, "version": version}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "pricing.rule.publish", "pricing_rule", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": version, "replayed": replayed})
}

func (a ResourceAPI) disablePricing(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var row struct {
		ID        uint64
		StationID uint64
		Status    string
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_rule").Where("id=? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if row.StationID != 0 {
			var station Station
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", row.StationID).Take(&station).Error; err != nil {
				return err
			}
		}
		result := tx.Table("pricing_rule").Where("id=? AND status='active' AND deleted_at IS NULL", id).Update("status", "disabled")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return resourceAudit(tx, p, "pricing.rule.disable", "pricing_rule", id, gin.H{"status": "active"}, gin.H{"status": "disabled"}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": "disabled"})
}
