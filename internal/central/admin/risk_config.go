package admin

import (
	"encoding/json"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerRiskConfig 保留钱包风控参数的配置入口。
func (a ResourceAPI) registerRiskConfig(r *gin.Engine) {
	r.PUT("/api/v1/admin/risk-config", a.Auth.Require("alert.risk_config.update"), a.saveRiskConfig)
}

// normalizeBool 保留管理员列表已有的布尔值归一化调用。
func normalizeBool(value bool) bool { return value }

// saveRiskConfig 按键新增或覆盖一条风控配置，值以 JSON 原文存储并记审计。
func (a ResourceAPI) saveRiskConfig(c *gin.Context) {
	var in struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Key, 64) || in.Value == nil {
		httpapi.BadRequest(c, "请填写有效的风控配置键和值")
		return
	}
	encoded, err := json.Marshal(in.Value)
	if err != nil {
		httpapi.BadRequest(c, "配置值无法序列化")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("risk_config").Clauses(clause.OnConflict{UpdateAll: true}).
			Create(map[string]any{"key": in.Key, "value": string(encoded), "updated_by": profile.ID}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "update", "risk_config", 0, nil, in, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"key": in.Key})
}
