package settlement

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 提现（withdraw_request）是结算域的资金出口：分账参与方把已结算金额提走。
// 申请、审核、打款三个动作的写入语义归属本包，admin 仅保留 HTTP 编排与审计。

// ErrWithdrawConflict 表示提现单的状态、余额或内容冲突。
var ErrWithdrawConflict = errors.New("withdraw request conflict")

// WithdrawRow 映射 withdraw_request 的提现单。
// 申请、审核和打款分别校验资金余额，数据库列显式映射到 Go 字段。
type WithdrawRow struct {
	ID           uint64  `gorm:"column:id" json:"id"`                       // 提现单主键 ID
	WithdrawNo   string  `gorm:"column:withdraw_no" json:"withdraw_no"`     // 提现单号,形如 WD + UUID 前 24 位大写,全局唯一
	PartyID      uint64  `gorm:"column:party_id" json:"party_id"`           // 分账参与方 ID,关联管理库 split_party.id
	PartyCode    string  `gorm:"column:party_code" json:"party_code"`       // 参与方编码快照,申请时从 split_party 复制,便于对账不依赖联表
	AmountCents  int64   `gorm:"column:amount_cents" json:"amount_cents"`   // 申请金额,单位分,必须为正数且不超过可提现余额
	BankAccount  *string `gorm:"column:bank_account" json:"bank_account"`   // 收款账号快照(可空),来自参与方资料
	BankName     *string `gorm:"column:bank_name" json:"bank_name"`         // 开户行名称快照(可空)
	Status       string  `gorm:"column:status" json:"status"`               // 单据状态:pending 待审核 / approved 审核通过 / rejected 已驳回 / paid 已打款 / failed 失败
	ReviewedBy   *uint64 `gorm:"column:reviewed_by" json:"reviewed_by"`     // 审核人管理员 ID(可空),审核后才写入
	ReviewedAt   *string `gorm:"column:reviewed_at" json:"reviewed_at"`     // 审核时间(可空),形如 2006-01-02 15:04:05.000
	RejectReason *string `gorm:"column:reject_reason" json:"reject_reason"` // 驳回原因(可空),最长 255 字符
	PaidAt       *string `gorm:"column:paid_at" json:"paid_at"`             // 打款时间(可空),仅 paid 状态有值
	Note         *string `gorm:"column:note" json:"note"`                   // 操作备注(可空),最长 255 字符
	CreatedAt    string  `gorm:"column:created_at" json:"created_at"`       // 申请创建时间,UTC
}

// WithdrawStore 承载提现单的生命周期写入。DB 为计费库句柄；方法不自行开事务：
// 调用方须传入已挂载审计与幂等语义的 *gorm.DB 事务。
type WithdrawStore struct {
	DB *gorm.DB
}

// AvailableCents 返回该参与方当前可提现金额（单位分），等于已结算金额减去未完结申请占用的金额。
func (s WithdrawStore) AvailableCents(ctx context.Context, tx *gorm.DB, partyID uint64) (int64, error) {
	return s.AvailableCentsExcluding(ctx, tx, partyID, 0)
}

// AvailableCentsExcluding 就是 AvailableCents，只是把 excludeID 这张单据从占用里剔除，
// 供审核通过与打款两个动作使用，否则“本单占的余额”会把自己挡住。
func (s WithdrawStore) AvailableCentsExcluding(ctx context.Context, tx *gorm.DB, partyID, excludeID uint64) (int64, error) {
	if tx == nil {
		tx = s.DB.WithContext(ctx)
	}
	var earned int64
	// settlement_party_amount 不包含分区列，仅按 settlement_id 连接。
	if err := tx.Table("settlement_party_amount AS p").
		Joins("JOIN settlement AS st ON st.id = p.settlement_id").
		Where("p.party_id = ? AND st.status = 'paid'", partyID).
		Select("COALESCE(SUM(p.amount_cents),0)").Scan(&earned).Error; err != nil {
		return 0, err
	}
	claimedQuery := tx.Table("withdraw_request").Where("party_id = ? AND status IN ('pending','approved')", partyID)
	if excludeID != 0 {
		claimedQuery = claimedQuery.Where("id <> ?", excludeID)
	}
	var claimed int64
	if err := claimedQuery.Select("COALESCE(SUM(amount_cents),0)").Scan(&claimed).Error; err != nil {
		return 0, err
	}
	if claimed > earned {
		return 0, nil
	}
	return earned - claimed, nil
}

// WithdrawInput 是提现申请的最小事实集合。
type WithdrawInput struct {
	RequestID   string
	PartyID     uint64
	AmountCents int64
	Note        *string
}

// withdrawNo 由幂等请求号推导提现单号：WD + 去掉短横线的大写 UUID 前 24 位。
func withdrawNo(requestID string) string {
	return "WD" + strings.ToUpper(strings.ReplaceAll(requestID, "-", ""))[:24]
}

// Create 新建提现申请：在写单据的同一个事务里重算可提现余额，并发申请不会把余额提穿；
// 相同请求号且内容一致的重放返回原单号，不重复占用余额。
func (s WithdrawStore) Create(ctx context.Context, tx *gorm.DB, in WithdrawInput) (string, error) {
	no := withdrawNo(in.RequestID)
	// 请求号及内容一致的重放返回已有单据，不重复创建。
	var existing WithdrawRow // 幂等重放时命中的原单据,用来比对金额与参与方是否一致
	found := tx.WithContext(ctx).Table("withdraw_request").Where("withdraw_no = ?", no).Take(&existing)
	if found.Error == nil {
		if existing.AmountCents != in.AmountCents || existing.PartyID != in.PartyID {
			return no, ErrWithdrawConflict
		}
		return no, nil
	}
	if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
		return no, found.Error
	}
	var party struct {
		ID         uint64  `gorm:"column:id"`                // 参与方主键 ID
		Code       string  `gorm:"column:party_code"`        // 参与方编码
		Name       string  `gorm:"column:party_name"`        // 参与方名称
		BankAcc    *string `gorm:"column:bank_account"`      // 收款账号(可空)
		BankName   *string `gorm:"column:bank_name"`         // 开户行(可空)
		TemplateID uint64  `gorm:"column:split_template_id"` // 绑定的分账模板 ID,模板已删除或非 active 时不允许提现
	}
	// 参与方身份与计费共享 central_db，在同一事务中核对。
	// split_party 没有 deleted_at：下线这件事记在父级模板上。
	if err := tx.Table("split_party").
		Select("id, party_code, party_name, bank_account, bank_name, split_template_id").
		Where("id = ?", in.PartyID).Take(&party).Error; err != nil {
		return no, err
	}
	var template struct {
		Status  string  `gorm:"column:status"`     // 分账模板状态,只有 active 允许提现
		Deleted *string `gorm:"column:deleted_at"` // 分账模板软删时间(可空),非空表示已下线
	}
	if err := tx.Table("split_template").
		Select("status, deleted_at").Where("id = ?", party.TemplateID).Take(&template).Error; err != nil {
		return no, err
	}
	if template.Deleted != nil || template.Status != "active" {
		return no, ErrWithdrawConflict
	}
	available, err := s.AvailableCents(ctx, tx, in.PartyID)
	if err != nil {
		return no, err
	}
	if available < in.AmountCents {
		return no, ErrWithdrawConflict
	}
	return no, tx.Table("withdraw_request").Create(map[string]any{
		"withdraw_no": no, "party_id": in.PartyID, "party_code": party.Code, "amount_cents": in.AmountCents,
		"bank_account": party.BankAcc, "bank_name": party.BankName, "status": "pending", "note": in.Note,
	}).Error
}

// WithdrawDecision 是提现审核决定；Approve 非 nil 时按布尔值通过或要求驳回理由。
type WithdrawDecision struct {
	Approve *bool
	Reason  *string
}

// Decide 审核提现：锁定记录并校验状态；通过前排除当前申请后重算可提现余额，
// 余额不足时返回冲突。返回的 action 为审计动作名（approve / reject）。
func (s WithdrawStore) Decide(ctx context.Context, tx *gorm.DB, no string, reviewerID uint64, in WithdrawDecision) (string, uint64, error) {
	var row WithdrawRow
	if err := tx.WithContext(ctx).Table("withdraw_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("withdraw_no = ?", no).Take(&row).Error; err != nil {
		return "", 0, err
	}
	values := map[string]any{"reviewed_by": reviewerID, "reviewed_at": gorm.Expr("UTC_TIMESTAMP(3)")}
	action := "approve"
	if in.Reason != nil && utf8.RuneCountInString(*in.Reason) > 255 {
		return action, row.ID, ErrWithdrawConflict
	}
	switch {
	case in.Approve != nil && *in.Approve:
		if row.Status != "pending" {
			return action, row.ID, ErrWithdrawConflict
		}
		// 本单 pending 金额已计入占用，余额校验时排除本单，避免重复计算。
		available, err := s.AvailableCentsExcluding(ctx, tx, row.PartyID, row.ID)
		if err != nil {
			return action, row.ID, err
		}
		if available < row.AmountCents {
			return action, row.ID, ErrWithdrawConflict
		}
		values["status"] = "approved"
	case in.Reason != nil:
		// 驳回必须带理由，并且从两种未完结状态里都能发起。
		if strings.TrimSpace(*in.Reason) == "" {
			return action, row.ID, ErrWithdrawConflict
		}
		if row.Status != "pending" && row.Status != "approved" {
			return action, row.ID, ErrWithdrawConflict
		}
		action, values["status"], values["reject_reason"] = "reject", "rejected", strings.TrimSpace(*in.Reason)
	case in.Approve != nil:
		// 驳回必须填写理由。
		return action, row.ID, ErrWithdrawConflict
	}
	return action, row.ID, tx.Table("withdraw_request").Where("withdraw_no = ? AND status = ?", no, row.Status).Updates(values).Error
}

// Pay 登记打款结果：只有已通过的单据能置为 paid，且登记前会再次剔除本单校验余额；
// 已打款的单据重复调用返回 alreadyPaid=true，不会重复出款。
func (s WithdrawStore) Pay(ctx context.Context, tx *gorm.DB, no string, note *string) (uint64, bool, error) {
	var row WithdrawRow
	if err := tx.WithContext(ctx).Table("withdraw_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("withdraw_no = ?", no).Take(&row).Error; err != nil {
		return 0, false, err
	}
	if row.Status == "paid" {
		// 已经打款过：只回报已记录的状态，不再出一次款。
		return row.ID, true, nil
	}
	if row.Status != "approved" {
		return row.ID, false, ErrWithdrawConflict
	}
	// 打款校验排除本张已审批单据的占用金额，避免重复扣减可用余额。
	available, err := s.AvailableCentsExcluding(ctx, tx, row.PartyID, row.ID)
	if err != nil {
		return row.ID, false, err
	}
	if available < row.AmountCents {
		return row.ID, false, ErrWithdrawConflict
	}
	values := map[string]any{"status": "paid", "paid_at": gorm.Expr("UTC_TIMESTAMP(3)")}
	if note != nil && utf8.RuneCountInString(*note) > 255 {
		return row.ID, false, ErrWithdrawConflict
	} else if note != nil {
		values["note"] = *note
	}
	return row.ID, false, tx.Table("withdraw_request").Where("withdraw_no = ? AND status = 'approved'", no).Updates(values).Error
}
