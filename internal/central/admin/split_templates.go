package admin

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// resourceCodePattern 守住那些还留在系统里、由运营手工填写的业务编码。
// 站点编码原先也走这里，已随 0044 迁移删掉；
// 现在剩下的只有分账模板编码和参与方编码。
// resourceCodePattern 约束运营手工填写的业务编码：只允许字母、数字、下划线、短横，1–64 位。
// 站点编码原先也走这里，已随 0044 迁移删除，现在只剩分账模板编码和参与方编码。
var resourceCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// splitTemplateRow 是分账模板（split_template 表）的行映射。
// 分账模板定义一份多方分账方案，由站点绑定后被结算流程读取，本身不含金额。
type splitTemplateRow struct {
	ID     uint64 `json:"id" gorm:"column:id"`         // 模板主键
	Code   string `json:"code" gorm:"column:code"`     // 模板业务编码，全局唯一，编码创建后不再可改
	Name   string `json:"name" gorm:"column:name"`     // 模板名称，不超过 128 个字符
	Mode   string `json:"mode" gorm:"column:mode"`     // 分账模式：mode_a 电费与服务费全部分账；mode_b 仅服务费分账、电费全额归运营商
	Status string `json:"status" gorm:"column:status"` // 模板状态：active 可被站点绑定；disabled 不能被新站点绑定
}

// splitPartyRow 是分账参与方（split_party 表）的行映射：一份模板下的一个分成方及其比例与收款信息。
type splitPartyRow struct {
	ID              uint64  `json:"id" gorm:"column:id"`                         // 参与方主键
	SplitTemplateID uint64  `json:"-" gorm:"column:split_template_id"`           // 所属分账模板 ID，不对外输出
	PartyCode       string  `json:"party_code" gorm:"column:party_code"`         // 参与方编码，同一模板下唯一
	PartyName       string  `json:"party_name" gorm:"column:party_name"`         // 参与方名称，如"万达物业""平台运营"
	RatioBP         uint32  `json:"ratio_bp" gorm:"column:ratio_bp"`             // 分账比例，单位基点（万分之一），同一模板下所有参与方合计恰好 10000
	BankAccount     *string `json:"-" gorm:"column:bank_account"`                // 收款银行账号，完整值不出接口（只回后四位）；nil 表示未登记
	BankName        *string `json:"bank_name,omitempty" gorm:"column:bank_name"` // 开户行；nil 表示未登记
}

// splitPartyInput 是分账参与方的写入入参，比例用基点整数表达，避免浮点误差把 10000 凑不齐。
type splitPartyInput struct {
	PartyCode   string  `json:"party_code"`   // 参与方编码，同一模板下不可重复，须匹配 resourceCodePattern
	PartyName   string  `json:"party_name"`   // 参与方名称，非空且不超过 128 个字符
	RatioBP     uint32  `json:"ratio_bp"`     // 分账比例（基点），必须大于 0 且小于 10000，同一模板合计恰好 10000
	BankAccount *string `json:"bank_account"` // 收款银行账号，nil 表示不登记；长度不超过 64
	BankName    *string `json:"bank_name"`    // 开户行，nil 表示不登记；不超过 128 个字符
}

// splitTemplateDetail 是分账模板的读取视图：模板本体加上展开后的参与方列表。
// 参与方在输出时已做脱敏，账号只保留后四位。
type splitTemplateDetail struct {
	splitTemplateRow
	Parties []gin.H `json:"parties"` // 参与方列表，账号已截断为后四位
}

// errSplitTemplateReferenced 表示模板已被站点绑定，模式和参与方就此冻结（名称仍可改）。
var errSplitTemplateReferenced = errors.New("分账模板已绑定站点，参与方和模式不可修改；请新建模板")

// errSplitParties 表示参与方数量不在 2–8 之间，或比例合计不等于 10000 基点。
var errSplitParties = errors.New("分账参与方须为 2–8 个，比例合计须为 10000 基点")

// validSplitParties 校验参与方集合：数量 2–8、编码不重复、名称与账号长度合规、
// 每方比例大于 0 且小于 10000，且全部比例合计恰好 10000 基点。
func validSplitParties(parties []splitPartyInput) bool {
	if len(parties) < 2 || len(parties) > 8 {
		return false
	}
	codes := make(map[string]bool, len(parties))
	var sum uint64
	for _, party := range parties {
		if !resourceCodePattern.MatchString(party.PartyCode) || codes[party.PartyCode] ||
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

// registerSplitTemplates 注册分账模板的五个后台接口：
// 读取要 finance.read，创建模板和改模式要 finance.split_template.create，换参与方要 finance.split_party.create。
func (a ResourceAPI) registerSplitTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/split-templates", a.Auth.Require("finance.read"), a.splitTemplates)
	r.GET("/api/v1/admin/settings/split-templates/:id", a.Auth.Require("finance.read"), a.splitTemplate)
	r.GET("/api/v1/admin/settings/split-templates/:id/parties", a.Auth.Require("finance.read"), a.splitTemplateParties)
	r.POST("/api/v1/admin/settings/split-templates", a.Auth.Require("finance.split_template.create"), a.createSplitTemplate)
	r.PUT("/api/v1/admin/settings/split-templates/:id", a.Auth.Require("finance.split_template.create"), a.updateSplitTemplate)
	r.POST("/api/v1/admin/settings/split-templates/:id/parties", a.Auth.Require("finance.split_party.create"), a.replaceSplitParties)
}

// splitTemplates 分页返回分账模板列表，支持按状态（active / disabled）和编码/名称关键词过滤。
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

// createSplitTemplate 新建分账模板：校验编码、名称、模式（mode_a / mode_b）和整组参与方，
// 模板与参与方在同一个事务里写入，初始状态 active。成功后直接回显完整详情。
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
	if !resourceCodePattern.MatchString(input.Code) || strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 128 ||
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

// insertSplitParties 把一组参与方逐条写入指定模板，全成或全不成（由外层事务保证）。
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

// splitPartyAudit 把参与方整理成可写审计的形态：只留编码、名称、比例和账号后四位，完整账号不进审计日志。
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

// updateSplitTemplate 修改模板的名称、模式和状态。约束：一旦被站点绑定，模式和状态就不能再改
// （结算时读的是站点当时绑定的模板），只能改名称；改回 active 之前会先复核参与方比例是否仍合法。
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

// replaceSplitParties 整体替换某份模板的参与方：先校验新集合合法，再确认模板尚未被站点绑定，
// 然后在锁内先删后插，并把改前改后两组参与方（脱敏后）写进审计。
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

// splitTemplateBound 判断模板是否已被某个未删除的站点绑定，是"模板冻结"这条规则的判定入口。
func splitTemplateBound(tx *gorm.DB, id uint64) (bool, error) {
	var count int64
	err := tx.Table("station").Where("split_template_id = ? AND deleted_at IS NULL", id).Count(&count).Error
	return count > 0, err
}

// splitTemplateRatiosValid 读出模板下现存参与方并按同一套规则复核一遍，
// 用于"重新启用模板"和"站点绑定模板"这两个入口。
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

// splitTemplateFailure 分账侧的失败出口：模板已绑定和参与方非法都是 409 业务冲突，
// 其余错误走通用处理。
func splitTemplateFailure(c *gin.Context, err error) {
	if errors.Is(err, errSplitTemplateReferenced) || errors.Is(err, errSplitParties) {
		httpapi.Write(c, 409, 2009, err.Error(), nil)
		return
	}
	resourceFailure(c, err)
}

// requireUsableSplitTemplate 在给站点绑定模板之前加行锁读一遍：
// 模板必须存在、未删除、状态 active，且参与方比例仍然合法，避免把一份不可用的模板绑到站上。
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

// 已有的站点只有在还没产生任何充值或充电记录之前才允许改绑。
// 结算在执行时才去读绑定的模板；保住历史，
// 也就避免了让一笔延迟到账的结算按另一个模板重新分配。

// bindStationSplitTemplate 给站点改绑分账模板。约束：必须带 expected_template_id 做乐观锁（未绑定时传 0），
// 目标模板必须可用；站点只允许在没有任何充值订单、没有充电订单、且已被停用时才可改绑，
// 避免历史（含延迟到账的）结算被按新模板重新分配。改绑完成后回显站点详情。
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
