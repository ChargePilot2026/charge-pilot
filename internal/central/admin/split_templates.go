package admin

import (
	"errors"
	"strings"
	"time"

	settlementpkg "github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerSplitTemplates 注册分账模板的五个后台接口：
// 读取要 finance.read，创建模板和改模式要 finance.split_template.create，换参与方要 finance.split_party.create。
// 模板与参与方的写入语义归属 settlement 家族，本文件只做 HTTP 编排、读取视图与审计。
func (a ResourceAPI) registerSplitTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/split-templates", a.Auth.Require("finance.read"), a.splitTemplates)
	r.GET("/api/v1/admin/settings/split-templates/:id", a.Auth.Require("finance.read"), a.splitTemplate)
	r.GET("/api/v1/admin/settings/split-templates/:id/parties", a.Auth.Require("finance.read"), a.splitTemplateParties)
	r.POST("/api/v1/admin/settings/split-templates", a.Auth.Require("finance.split_template.create"), a.createSplitTemplate)
	r.PUT("/api/v1/admin/settings/split-templates/:id", a.Auth.Require("finance.split_template.create"), a.updateSplitTemplate)
	r.POST("/api/v1/admin/settings/split-templates/:id/parties", a.Auth.Require("finance.split_party.create"), a.replaceSplitParties)
}

// splitTemplateDetail 是分账模板的读取视图：模板本体加上展开后的参与方列表。
// 参与方在输出时已做脱敏，账号只保留后四位。
type splitTemplateDetail struct {
	settlementpkg.SplitTemplateRow
	Parties []gin.H `json:"parties"` // 参与方列表，账号已截断为后四位
}

// splitTemplates 分页返回分账模板列表，支持按状态（active / disabled）和编码/名称关键词过滤。
func (a ResourceAPI) splitTemplates(c *gin.Context) {
	page, ok := parsePage(c, "active disabled")
	if !ok {
		return
	}
	out := Page[settlementpkg.SplitTemplateRow]{Items: []settlementpkg.SplitTemplateRow{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("split_template").Where("deleted_at IS NULL")
	if page.Status != "" {
		query = query.Where("status = ?", page.Status)
	}
	if page.Keyword != "" {
		pattern := likePattern(page.Keyword)
		query = query.Where("(code LIKE ? ESCAPE '!' OR name LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := query.Select("id,code,name,mode,status").Order("id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// splitTemplate 返回单份分账模板详情（含参与方），站点绑定接口改绑完成后也复用它回显。
func (a ResourceAPI) splitTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	detail, err := a.loadSplitTemplate(c, id)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, detail)
}

// splitTemplateParties 只返回某份模板的参与方列表，供不需要模板本体的场景使用。
func (a ResourceAPI) splitTemplateParties(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	detail, err := a.loadSplitTemplate(c, id)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": detail.Parties})
}

// loadSplitTemplate 读出一份模板及其参与方，是详情、参与方列表、创建/修改回显和站点绑定回显的共同读取路径。
// 输出时银行账号一律截断为后四位，完整账号只留在库里。
func (a ResourceAPI) loadSplitTemplate(c *gin.Context, id uint64) (splitTemplateDetail, error) {
	var row settlementpkg.SplitTemplateRow
	db := a.Store.AdminDB.WithContext(c.Request.Context())
	if err := db.Table("split_template").Select("id,code,name,mode,status").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		return splitTemplateDetail{}, err
	}
	parties := []settlementpkg.SplitPartyRow{}
	if err := db.Table("split_party").Where("split_template_id = ?", id).Order("id").Find(&parties).Error; err != nil {
		return splitTemplateDetail{}, err
	}
	detail := splitTemplateDetail{SplitTemplateRow: row, Parties: make([]gin.H, 0, len(parties))}
	for _, party := range parties {
		detail.Parties = append(detail.Parties, partyMasked(party))
	}
	return detail, nil
}

// partyMasked 把参与方输出形态截断账号为后四位，完整账号不出接口。
func partyMasked(party settlementpkg.SplitPartyRow) gin.H {
	lastFour := ""
	if party.BankAccount != nil {
		lastFour = *party.BankAccount
		if len(lastFour) > 4 {
			lastFour = lastFour[len(lastFour)-4:]
		}
	}
	return gin.H{"id": party.ID, "party_code": party.PartyCode, "party_name": party.PartyName,
		"ratio_bp": party.RatioBP, "bank_account_last4": lastFour, "bank_name": party.BankName}
}

// createSplitTemplate 新建分账模板：校验编码、名称、模式（mode_a / mode_b）和整组参与方，
// 模板与参与方在同一个事务里写入。成功后直接回显完整详情。
func (a ResourceAPI) createSplitTemplate(c *gin.Context) {
	var input struct {
		Code    string                          `json:"code"`
		Name    string                          `json:"name"`
		Mode    string                          `json:"mode"`
		Parties []settlementpkg.SplitPartyInput `json:"parties"`
	}
	if !decodeResource(c, &input) {
		return
	}
	if !settlementpkg.CodePattern.MatchString(input.Code) || strings.TrimSpace(input.Name) == "" ||
		!oneOf(input.Mode, "mode_a mode_b") || !settlementpkg.ValidParties(input.Parties) {
		httpapi.BadRequest(c, settlementpkg.ErrSplitParties.Error())
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	var templateID uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		templateID, err = (settlementpkg.SplitTemplateStore{}).Create(tx, input.Code, input.Name, input.Mode, input.Parties)
		if err != nil {
			return err
		}
		return resourceAudit(tx, profile, "create", "split_template", templateID, nil,
			gin.H{"code": input.Code, "mode": input.Mode, "parties": splitPartyAudit(input.Parties)}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		splitTemplateFailure(c, err)
		return
	}
	detail, err := a.loadSplitTemplate(c, templateID)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, detail)
}

// splitPartyAudit 把参与方整理成可写审计的形态：只留编码、名称、比例和账号后四位，完整账号不进审计日志。
func splitPartyAudit(parties []settlementpkg.SplitPartyInput) []gin.H {
	out := make([]gin.H, 0, len(parties))
	for _, party := range parties {
		lastFour := ""
		if party.BankAccount != nil {
			lastFour = *party.BankAccount
			if len(lastFour) > 4 {
				lastFour = lastFour[len(lastFour)-4:]
			}
		}
		out = append(out, gin.H{"party_code": party.PartyCode, "party_name": party.PartyName, "ratio_bp": party.RatioBP,
			"bank_account_last4": lastFour})
	}
	return out
}

// updateSplitTemplate 修改模板的名称、模式和状态；改绑约束与比例复核归属 settlement 家族。
func (a ResourceAPI) updateSplitTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Name   string `json:"name"`
		Mode   string `json:"mode"`
		Status string `json:"status"`
	}
	if !decodeResource(c, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" ||
		(input.Mode != "mode_a" && input.Mode != "mode_b") || (input.Status != "active" && input.Status != "disabled") {
		httpapi.BadRequest(c, "分账模板参数无效")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		before, after, err := (settlementpkg.SplitTemplateStore{}).Update(tx, id, input.Name, input.Mode, input.Status)
		if err != nil {
			return err
		}
		return resourceAudit(tx, profile, "update", "split_template", id, before, after, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		splitTemplateFailure(c, err)
		return
	}
	a.splitTemplate(c)
}

// replaceSplitParties 整体替换某份模板的参与方：改前改后两组参与方（脱敏后）写进审计。
func (a ResourceAPI) replaceSplitParties(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Parties []settlementpkg.SplitPartyInput `json:"parties"`
	}
	if !decodeResource(c, &input) {
		return
	}
	if !settlementpkg.ValidParties(input.Parties) {
		httpapi.BadRequest(c, settlementpkg.ErrSplitParties.Error())
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		before, err := (settlementpkg.SplitTemplateStore{}).ReplaceParties(tx, id, input.Parties)
		if err != nil {
			return err
		}
		old := make([]settlementpkg.SplitPartyInput, 0, len(before))
		for _, party := range before {
			old = append(old, settlementpkg.SplitPartyInput{PartyCode: party.PartyCode, PartyName: party.PartyName, RatioBP: party.RatioBP, BankAccount: party.BankAccount})
		}
		return resourceAudit(tx, profile, "replace_parties", "split_template", id,
			splitPartyAudit(old), splitPartyAudit(input.Parties), c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		splitTemplateFailure(c, err)
		return
	}
	a.splitTemplate(c)
}

// splitTemplateFailure 分账侧的失败出口：模板已绑定和参与方非法都是 409 业务冲突，
// 其余错误走通用处理。
func splitTemplateFailure(c *gin.Context, err error) {
	if errors.Is(err, settlementpkg.ErrSplitTemplateReferenced) || errors.Is(err, settlementpkg.ErrSplitParties) {
		httpapi.Write(c, 409, 2009, err.Error(), nil)
		return
	}
	resourceFailure(c, err)
}

// 已有的站点只有在还没产生任何充值或充电记录之前才允许改绑。
// 结算在执行时才去读绑定的模板；保住历史，
// 也就避免了让一笔延迟到账的结算按另一个模板重新分配。

// bindStationSplitTemplate 给站点改绑分账模板。约束：必须带 expected_template_id 做乐观锁（未绑定时传 0），
// 目标模板必须可用（可用性判定归属 settlement 家族）；站点只允许在没有任何充值订单、
// 没有充电订单、且已被停用时才可改绑，避免历史（含延迟到账的）结算被按新模板重新分配。
// 改绑完成后回显站点详情。
func (a ResourceAPI) bindStationSplitTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		TemplateID         uint64  `json:"template_id"`
		ExpectedTemplateID *uint64 `json:"expected_template_id"`
	}
	if !decodeResource(c, &input) {
		return
	}
	if input.TemplateID == 0 || input.ExpectedTemplateID == nil {
		httpapi.BadRequest(c, "请提供模板 ID 和当前模板 ID；未绑定时当前模板 ID 为 0")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := settlementpkg.UsableTemplate(tx, input.TemplateID); err != nil {
			return err
		}
		var before struct {
			ID              uint64
			Status          string
			SplitTemplateID *uint64
			UpdatedAt       time.Time
		}
		if err := tx.Table("station").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		current := uint64(0)
		if before.SplitTemplateID != nil {
			current = *before.SplitTemplateID
		}
		if current != *input.ExpectedTemplateID {
			return errConflict
		}
		if current == input.TemplateID {
			return nil
		}
		if before.Status != "disabled" {
			return errConflict
		}
		devices := []string{}
		if err := tx.Table("device_meta").Where("station_id = ? AND deleted_at IS NULL", id).Pluck("device_id", &devices).Error; err != nil {
			return err
		}
		var intentHistory int64
		if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("charge_payment_intent").
			Where("station_id = ?", id).Count(&intentHistory).Error; err != nil {
			return err
		}
		if intentHistory > 0 {
			return errConflict
		}
		if len(devices) > 0 {
			if before.UpdatedAt.After(time.Now().UTC().Add(-5 * time.Minute)) {
				return errConflict
			}
			var chargeHistory int64
			if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("charge_order").
				Where("device_id IN ?", devices).Count(&chargeHistory).Error; err != nil {
				return err
			}
			if chargeHistory > 0 {
				return errConflict
			}
		}
		if err := tx.Table("station").Where("id = ?", id).Update("split_template_id", input.TemplateID).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "bind_split_template", "station", id,
			gin.H{"split_template_id": current}, gin.H{"split_template_id": input.TemplateID}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		splitTemplateFailure(c, err)
		return
	}
	a.station(c)
}
