package settlement

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 分账模板（split_template / split_party）是结算域的配置数据：结算执行时读取
// 站点绑定的模板并按参与方比例分配金额。写入语义归属本包，admin 仅保留
// HTTP 编排、读取视图与审计。

// ErrSplitTemplateReferenced 表示模板已被站点绑定，模式和参与方就此冻结（名称仍可改）。
var ErrSplitTemplateReferenced = errors.New("分账模板已绑定站点，参与方和模式不可修改；请新建模板")

// ErrSplitParties 表示参与方数量不在 2–8 之间，或比例合计不等于 10000 基点。
var ErrSplitParties = errors.New("分账参与方须为 2–8 个，比例合计须为 10000 基点")

// CodePattern 限制分账模板和参与方编码为 1–64 位字母、数字、下划线或短横线。
var CodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// SplitTemplateRow 是分账模板（split_template 表）的行映射。
// 分账模板定义一份多方分账方案，由站点绑定后被结算流程读取，本身不含金额。
type SplitTemplateRow struct {
	ID     uint64 `json:"id" gorm:"column:id"`         // 模板主键
	Code   string `json:"code" gorm:"column:code"`     // 模板业务编码，全局唯一，编码创建后不再可改
	Name   string `json:"name" gorm:"column:name"`     // 模板名称，不超过 128 个字符
	Mode   string `json:"mode" gorm:"column:mode"`     // 分账模式：mode_a 电费与服务费全部分账；mode_b 仅服务费分账、电费全额归运营商
	Status string `json:"status" gorm:"column:status"` // 模板状态：active 可被站点绑定；disabled 不能被新站点绑定
}

// SplitPartyRow 是分账参与方（split_party 表）的行映射：一份模板下的一个分成方及其比例与收款信息。
type SplitPartyRow struct {
	ID              uint64  `json:"id" gorm:"column:id"`                         // 参与方主键
	SplitTemplateID uint64  `json:"-" gorm:"column:split_template_id"`           // 所属分账模板 ID，不对外输出
	PartyCode       string  `json:"party_code" gorm:"column:party_code"`         // 参与方编码，同一模板下唯一
	PartyName       string  `json:"party_name" gorm:"column:party_name"`         // 参与方名称，如"万达物业""平台运营"
	RatioBP         uint32  `json:"ratio_bp" gorm:"column:ratio_bp"`             // 分账比例，单位基点（万分之一），同一模板下所有参与方合计恰好 10000
	BankAccount     *string `json:"-" gorm:"column:bank_account"`                // 收款银行账号，完整值不出接口（只回后四位）；nil 表示未登记
	BankName        *string `json:"bank_name,omitempty" gorm:"column:bank_name"` // 开户行；nil 表示未登记
}

// SplitPartyInput 是分账参与方写入参数，比例使用基点整数，避免浮点求和误差。
type SplitPartyInput struct {
	PartyCode   string  `json:"party_code"`   // 参与方编码，同一模板下不可重复，须匹配 CodePattern
	PartyName   string  `json:"party_name"`   // 参与方名称，非空且不超过 128 个字符
	RatioBP     uint32  `json:"ratio_bp"`     // 分账比例（基点），必须大于 0 且小于 10000，同一模板合计恰好 10000
	BankAccount *string `json:"bank_account"` // 收款银行账号，nil 表示不登记；长度不超过 64
	BankName    *string `json:"bank_name"`    // 开户行，nil 表示不登记；不超过 128 个字符
}

// ValidParties 校验参与方集合：数量 2–8、编码不重复、名称与账号长度合规、
// 每方比例大于 0 且小于 10000，且全部比例合计恰好 10000 基点。
func ValidParties(parties []SplitPartyInput) bool {
	if len(parties) < 2 || len(parties) > 8 {
		return false
	}
	codes := make(map[string]bool, len(parties))
	var sum uint64
	for _, party := range parties {
		if !CodePattern.MatchString(party.PartyCode) || codes[party.PartyCode] ||
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

// SplitTemplateStore 承载分账模板与参与方的写入。方法不自行开事务：
// 调用方须传入已挂载审计语义的 *gorm.DB 事务。
type SplitTemplateStore struct {
	DB *gorm.DB
}

// Create 新建分账模板并整组写入参与方，初始状态 active，返回模板 ID。
func (s SplitTemplateStore) Create(tx *gorm.DB, code, name, mode string, parties []SplitPartyInput) (uint64, error) {
	row := SplitTemplateRow{Code: code, Name: strings.TrimSpace(name), Mode: mode, Status: "active"}
	if err := tx.Table("split_template").Create(&row).Error; err != nil {
		return 0, err
	}
	return row.ID, insertSplitParties(tx, row.ID, parties)
}

// insertSplitParties 把一组参与方逐条写入指定模板，全成或全不成（由外层事务保证）。
func insertSplitParties(tx *gorm.DB, templateID uint64, parties []SplitPartyInput) error {
	for _, party := range parties {
		if err := tx.Table("split_party").Create(map[string]any{"split_template_id": templateID, "party_code": party.PartyCode,
			"party_name": strings.TrimSpace(party.PartyName), "ratio_bp": party.RatioBP,
			"bank_account": party.BankAccount, "bank_name": party.BankName}).Error; err != nil {
			return err
		}
	}
	return nil
}

// Update 修改模板的名称、模式和状态，返回改前改后的模板行供审计。
// 约束：一旦被站点绑定，模式和状态就不能再改（结算时读的是站点当时绑定的模板），
// 只能改名称；改回 active 之前会先复核参与方比例是否仍合法。
func (s SplitTemplateStore) Update(tx *gorm.DB, id uint64, name, mode, status string) (SplitTemplateRow, SplitTemplateRow, error) {
	var before SplitTemplateRow
	if err := tx.Table("split_template").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
		return before, SplitTemplateRow{}, err
	}
	if before.Mode != mode || before.Status != status {
		bound, err := TemplateBound(tx, id)
		if err != nil {
			return before, SplitTemplateRow{}, err
		}
		if bound {
			return before, SplitTemplateRow{}, ErrSplitTemplateReferenced
		}
	}
	if status == "active" {
		valid, err := RatiosValid(tx, id)
		if err != nil {
			return before, SplitTemplateRow{}, err
		}
		if !valid {
			return before, SplitTemplateRow{}, ErrSplitParties
		}
	}
	after := SplitTemplateRow{ID: id, Code: before.Code, Name: strings.TrimSpace(name), Mode: mode, Status: status}
	if err := tx.Table("split_template").Where("id = ?", id).Updates(map[string]any{"name": after.Name, "mode": after.Mode, "status": after.Status}).Error; err != nil {
		return before, SplitTemplateRow{}, err
	}
	return before, after, nil
}

// ReplaceParties 整体替换某份模板的参与方：先确认模板尚未被站点绑定，再在锁内先删后插。
// 返回改前参与方（脱敏由调用方处理）供审计。
func (s SplitTemplateStore) ReplaceParties(tx *gorm.DB, id uint64, parties []SplitPartyInput) ([]SplitPartyRow, error) {
	var row SplitTemplateRow
	if err := tx.Table("split_template").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		return nil, err
	}
	bound, err := TemplateBound(tx, id)
	if err != nil {
		return nil, err
	}
	if bound {
		return nil, ErrSplitTemplateReferenced
	}
	before := []SplitPartyRow{}
	if err := tx.Table("split_party").Where("split_template_id = ?", id).Order("id").Find(&before).Error; err != nil {
		return nil, err
	}
	if err := tx.Table("split_party").Where("split_template_id = ?", id).Delete(&SplitPartyRow{}).Error; err != nil {
		return nil, err
	}
	return before, insertSplitParties(tx, id, parties)
}

// TemplateBound 判断模板是否已被某个未删除的站点绑定，是"模板冻结"这条规则的判定入口。
func TemplateBound(tx *gorm.DB, id uint64) (bool, error) {
	var count int64
	err := tx.Table("station").Where("split_template_id = ? AND deleted_at IS NULL", id).Count(&count).Error
	return count > 0, err
}

// RatiosValid 读出模板下现存参与方并按同一套规则复核一遍，
// 用于"重新启用模板"和"站点绑定模板"这两个入口。
func RatiosValid(tx *gorm.DB, id uint64) (bool, error) {
	var parties []SplitPartyRow
	if err := tx.Table("split_party").Where("split_template_id = ?", id).Find(&parties).Error; err != nil {
		return false, err
	}
	inputs := make([]SplitPartyInput, 0, len(parties))
	for _, party := range parties {
		inputs = append(inputs, SplitPartyInput{PartyCode: party.PartyCode, PartyName: party.PartyName, RatioBP: party.RatioBP,
			BankAccount: party.BankAccount, BankName: party.BankName})
	}
	return ValidParties(inputs), nil
}

// UsableTemplate 在给站点绑定模板之前加行锁读一遍：
// 模板必须存在、未删除、状态 active，且参与方比例仍然合法，避免把一份不可用的模板绑到站上。
func UsableTemplate(tx *gorm.DB, id uint64) error {
	var row SplitTemplateRow
	if err := tx.Table("split_template").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = 'active' AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		return err
	}
	valid, err := RatiosValid(tx, id)
	if err != nil {
		return err
	}
	if !valid {
		return ErrSplitParties
	}
	return nil
}
