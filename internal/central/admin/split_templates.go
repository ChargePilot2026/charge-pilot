package admin

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type splitTemplateRow struct {
	ID     uint64 `json:"id" gorm:"column:id"`
	Code   string `json:"code" gorm:"column:code"`
	Name   string `json:"name" gorm:"column:name"`
	Mode   string `json:"mode" gorm:"column:mode"`
	Status string `json:"status" gorm:"column:status"`
}

type splitPartyRow struct {
	ID              uint64  `json:"id" gorm:"column:id"`
	SplitTemplateID uint64  `json:"-" gorm:"column:split_template_id"`
	PartyCode       string  `json:"party_code" gorm:"column:party_code"`
	PartyName       string  `json:"party_name" gorm:"column:party_name"`
	RatioBP         uint32  `json:"ratio_bp" gorm:"column:ratio_bp"`
	BankAccount     *string `json:"-" gorm:"column:bank_account"`
	BankName        *string `json:"bank_name,omitempty" gorm:"column:bank_name"`
}

type splitPartyInput struct {
	PartyCode   string  `json:"party_code"`
	PartyName   string  `json:"party_name"`
	RatioBP     uint32  `json:"ratio_bp"`
	BankAccount *string `json:"bank_account"`
	BankName    *string `json:"bank_name"`
}

type splitTemplateDetail struct {
	splitTemplateRow
	Parties []gin.H `json:"parties"`
}

var errSplitTemplateReferenced = errors.New("分账模板已绑定站点，参与方和模式不可修改；请新建模板")
var errSplitParties = errors.New("分账参与方须为 2–8 个，比例合计须为 10000 基点")

func validSplitParties(parties []splitPartyInput) bool {
	if len(parties) < 2 || len(parties) > 8 {
		return false
	}
	codes := make(map[string]bool, len(parties))
	var sum uint64
	for _, party := range parties {
		if !stationCodePattern.MatchString(party.PartyCode) || codes[party.PartyCode] ||
			strings.TrimSpace(party.PartyName) == "" || utf8.RuneCountInString(party.PartyName) > 128 ||
			party.RatioBP == 0 || party.RatioBP >= 10000 ||
			(party.BankAccount != nil && len(*party.BankAccount) > 64) ||
			(party.BankName != nil && utf8.RuneCountInString(*party.BankName) > 128) {
			return false
		}
		codes[party.PartyCode] = true
		sum += uint64(party.RatioBP)
	}
	return sum == 10000
}

func (a ResourceAPI) registerSplitTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/split-templates", a.Auth.Require("finance.read"), a.splitTemplates)
	r.GET("/api/v1/admin/settings/split-templates/:id", a.Auth.Require("finance.read"), a.splitTemplate)
	r.GET("/api/v1/admin/settings/split-templates/:id/parties", a.Auth.Require("finance.read"), a.splitTemplateParties)
	r.POST("/api/v1/admin/settings/split-templates", a.Auth.Require("finance.split_template.create"), a.createSplitTemplate)
	r.PUT("/api/v1/admin/settings/split-templates/:id", a.Auth.Require("finance.split_template.create"), a.updateSplitTemplate)
	r.POST("/api/v1/admin/settings/split-templates/:id/parties", a.Auth.Require("finance.split_party.create"), a.replaceSplitParties)
}

func (a ResourceAPI) splitTemplates(c *gin.Context) {
	page, ok := parsePage(c, "active disabled")
	if !ok {
		return
	}
	out := Page[splitTemplateRow]{Items: []splitTemplateRow{}, Page: page.Page, PageSize: page.PageSize}
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

func (a ResourceAPI) loadSplitTemplate(c *gin.Context, id uint64) (splitTemplateDetail, error) {
	var row splitTemplateRow
	db := a.Store.AdminDB.WithContext(c.Request.Context())
	if err := db.Table("split_template").Select("id,code,name,mode,status").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		return splitTemplateDetail{}, err
	}
	parties := []splitPartyRow{}
	if err := db.Table("split_party").Where("split_template_id = ?", id).Order("id").Find(&parties).Error; err != nil {
		return splitTemplateDetail{}, err
	}
	detail := splitTemplateDetail{splitTemplateRow: row, Parties: make([]gin.H, 0, len(parties))}
	for _, party := range parties {
		lastFour := ""
		if party.BankAccount != nil {
			lastFour = *party.BankAccount
			if len(lastFour) > 4 {
				lastFour = lastFour[len(lastFour)-4:]
			}
		}
		detail.Parties = append(detail.Parties, gin.H{"id": party.ID, "party_code": party.PartyCode, "party_name": party.PartyName,
			"ratio_bp": party.RatioBP, "bank_account_last4": lastFour, "bank_name": party.BankName})
	}
	return detail, nil
}

func (a ResourceAPI) createSplitTemplate(c *gin.Context) {
	var input struct {
		Code    string            `json:"code"`
		Name    string            `json:"name"`
		Mode    string            `json:"mode"`
		Parties []splitPartyInput `json:"parties"`
	}
	if !decodeResource(c, &input) {
		return
	}
	if !stationCodePattern.MatchString(input.Code) || strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 ||
		!oneOf(input.Mode, "mode_a mode_b") || input.Mode == "" || !validSplitParties(input.Parties) {
		httpapi.BadRequest(c, errSplitParties.Error())
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	var templateID uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		row := splitTemplateRow{Code: input.Code, Name: strings.TrimSpace(input.Name), Mode: input.Mode, Status: "active"}
		if err := tx.Table("split_template").Create(&row).Error; err != nil {
			return err
		}
		templateID = row.ID
		if err := insertSplitParties(tx, templateID, input.Parties); err != nil {
			return err
		}
		return resourceAudit(tx, profile, "create", "split_template", templateID, nil,
			gin.H{"code": row.Code, "mode": row.Mode, "parties": splitPartyAudit(input.Parties)}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	detail, err := a.loadSplitTemplate(c, templateID)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, detail)
}

func insertSplitParties(tx *gorm.DB, templateID uint64, parties []splitPartyInput) error {
	for _, party := range parties {
		if err := tx.Table("split_party").Create(map[string]any{"split_template_id": templateID, "party_code": party.PartyCode,
			"party_name": strings.TrimSpace(party.PartyName), "ratio_bp": party.RatioBP,
			"bank_account": party.BankAccount, "bank_name": party.BankName}).Error; err != nil {
			return err
		}
	}
	return nil
}

func splitPartyAudit(parties []splitPartyInput) []gin.H {
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
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 ||
		(input.Mode != "mode_a" && input.Mode != "mode_b") || (input.Status != "active" && input.Status != "disabled") {
		httpapi.BadRequest(c, "分账模板参数无效")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before splitTemplateRow
		if err := tx.Table("split_template").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if before.Mode != input.Mode || before.Status != input.Status {
			bound, err := splitTemplateBound(tx, id)
			if err != nil {
				return err
			}
			if bound {
				return errSplitTemplateReferenced
			}
		}
		if input.Status == "active" {
			valid, err := splitTemplateRatiosValid(tx, id)
			if err != nil {
				return err
			}
			if !valid {
				return errSplitParties
			}
		}
		after := splitTemplateRow{ID: id, Code: before.Code, Name: strings.TrimSpace(input.Name), Mode: input.Mode, Status: input.Status}
		if err := tx.Table("split_template").Where("id = ?", id).Updates(map[string]any{"name": after.Name, "mode": after.Mode, "status": after.Status}).Error; err != nil {
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

func (a ResourceAPI) replaceSplitParties(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Parties []splitPartyInput `json:"parties"`
	}
	if !decodeResource(c, &input) {
		return
	}
	if !validSplitParties(input.Parties) {
		httpapi.BadRequest(c, errSplitParties.Error())
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row splitTemplateRow
		if err := tx.Table("split_template").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return err
		}
		bound, err := splitTemplateBound(tx, id)
		if err != nil {
			return err
		}
		if bound {
			return errSplitTemplateReferenced
		}
		before := []splitPartyRow{}
		if err := tx.Table("split_party").Where("split_template_id = ?", id).Order("id").Find(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("split_party").Where("split_template_id = ?", id).Delete(&splitPartyRow{}).Error; err != nil {
			return err
		}
		if err := insertSplitParties(tx, id, input.Parties); err != nil {
			return err
		}
		old := make([]splitPartyInput, 0, len(before))
		for _, party := range before {
			old = append(old, splitPartyInput{PartyCode: party.PartyCode, PartyName: party.PartyName, RatioBP: party.RatioBP, BankAccount: party.BankAccount})
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

func splitTemplateBound(tx *gorm.DB, id uint64) (bool, error) {
	var count int64
	err := tx.Table("station").Where("split_template_id = ? AND deleted_at IS NULL", id).Count(&count).Error
	return count > 0, err
}

func splitTemplateRatiosValid(tx *gorm.DB, id uint64) (bool, error) {
	var parties []splitPartyRow
	if err := tx.Table("split_party").Where("split_template_id = ?", id).Find(&parties).Error; err != nil {
		return false, err
	}
	inputs := make([]splitPartyInput, 0, len(parties))
	for _, party := range parties {
		inputs = append(inputs, splitPartyInput{PartyCode: party.PartyCode, PartyName: party.PartyName, RatioBP: party.RatioBP,
			BankAccount: party.BankAccount, BankName: party.BankName})
	}
	return validSplitParties(inputs), nil
}

func splitTemplateFailure(c *gin.Context, err error) {
	if errors.Is(err, errSplitTemplateReferenced) || errors.Is(err, errSplitParties) {
		httpapi.Write(c, 409, 2009, err.Error(), nil)
		return
	}
	resourceFailure(c, err)
}

func requireUsableSplitTemplate(tx *gorm.DB, id uint64) error {
	var row splitTemplateRow
	if err := tx.Table("split_template").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = 'active' AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		return err
	}
	valid, err := splitTemplateRatiosValid(tx, id)
	if err != nil {
		return err
	}
	if !valid {
		return errSplitParties
	}
	return nil
}

// Rebinding an existing station is allowed only before it has any payment or
// charge history. Settlement reads the bound template when it runs; preserving
// history avoids reallocating a delayed settlement under a different template.
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
		if err := requireUsableSplitTemplate(tx, input.TemplateID); err != nil {
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
