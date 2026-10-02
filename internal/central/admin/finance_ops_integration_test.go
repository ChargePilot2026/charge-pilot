package admin

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func openFinanceDB(t *testing.T, key string) *gorm.DB {
	t.Helper()
	db, err := dbconn.Open(context.Background(), os.Getenv(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// settledParty 创建 active 分账模板和 paid 结算单，为参与方提供可提现余额。
func settledParty(t *testing.T, ctx context.Context, code string, ratio int32, pool int64) (partyID uint64) {
	t.Helper()
	adminDB := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	billingDB := openFinanceDB(t, "TEST_BILLING_DATABASE_URL")
	suffix := uuid.NewString()[:8]
	template := "tpl-" + suffix
	party := "party-" + suffix
	// 按本轮夹具标识清理提现、参与方金额、结算单、参与方和模板，先删依赖记录再删主记录。
	t.Cleanup(func() {
		statements := []struct {
			db    *gorm.DB
			query string
			args  []any
		}{
			// 提现夹具按参与方编号清理，与建单时使用的编号保持一致。
			{billingDB, "DELETE FROM withdraw_request WHERE party_code = ?", []any{code}},
			{billingDB, "DELETE FROM settlement_party_amount WHERE settlement_id IN (SELECT id FROM settlement WHERE settlement_no = ?)", []any{"STL-" + suffix}},
			{billingDB, "DELETE FROM settlement WHERE settlement_no = ?", []any{"STL-" + suffix}},
			{adminDB, "DELETE FROM split_party WHERE split_template_id IN (SELECT id FROM split_template WHERE code = ?)", []any{template}},
			{adminDB, "DELETE FROM split_template WHERE code = ?", []any{template}},
		}
		for _, statement := range statements {
			if err := statement.db.Exec(statement.query, statement.args...).Error; err != nil {
				t.Errorf("清理分账夹具失败 (%v): %s", err, statement.query)
			}
		}
	})
	if err := adminDB.Exec("INSERT INTO split_template(code,name,mode,status) VALUES(?,?,'mode_a','active')", template, template).Error; err != nil {
		t.Fatal(err)
	}
	var templateID uint64
	adminDB.Table("split_template").Where("code = ?", template).Pluck("id", &templateID)
	if err := adminDB.Exec("INSERT INTO split_party(split_template_id,party_code,party_name,ratio_bp,bank_account,bank_name) VALUES(?,?,?,?,?,?)",
		templateID, party, party, ratio, "622200000099", "验收银行").Error; err != nil {
		t.Fatal(err)
	}
	adminDB.Table("split_party").Where("party_code = ?", party).Pluck("id", &partyID)
	settlementNo := "STL-" + suffix
	// 为夹具分配独立计费 ID，满足 uk_fee_generation 唯一约束。
	var highest struct {
		Value *int64 `gorm:"column:value"`
	}
	billingDB.Table("settlement").Select("MAX(fee_calculation_id) AS value").Take(&highest)
	calculationID := int64(9_000_000)
	if highest.Value != nil {
		calculationID = *highest.Value + 1
	}
	if err := billingDB.Exec(`INSERT INTO settlement(settlement_no,split_template_id,split_template_code,mode,fee_calculation_id,generation,
		order_no,total_cents,electric_cents,service_cents,split_pool_cents,status,created_month,paid_at)
		VALUES(?,?,?,'mode_a',?,1,?,?,0,?,?, 'paid', '2026-09-01', UTC_TIMESTAMP(3))`,
		settlementNo, templateID, template, calculationID, "ORD-"+suffix, pool, pool, pool).Error; err != nil {
		t.Fatal(err)
	}
	var settlementID uint64
	billingDB.Table("settlement").Where("settlement_no = ?", settlementNo).Pluck("id", &settlementID)
	if err := billingDB.Exec("INSERT INTO settlement_party_amount(settlement_id,party_id,party_code,party_name,ratio_bp,amount_cents,electric_cents,service_cents,status,generation) VALUES(?,?,?,?,?,?,0,?,'paid',1)",
		settlementID, partyID, party, party, ratio, pool, pool).Error; err != nil {
		t.Fatal(err)
	}
	return partyID
}

// 验证审批排除本单已占用的余额，避免将申请金额重复计入占用。
func TestWithdrawalCanBeApprovedAndPaid(t *testing.T) {
	if os.Getenv("TEST_BILLING_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	partyID := settledParty(t, ctx, "approve", 10000, 30000)
	billingDB := openFinanceDB(t, "TEST_BILLING_DATABASE_URL")
	withdrawStore := settlement.WithdrawStore{DB: billingDB}

	if _, err := withdrawStore.AvailableCents(ctx, nil, partyID); err != nil {
		t.Fatal(err)
	}
	no := "WD" + uuid.NewString()[:24]
	if err := billingDB.Table("withdraw_request").Create(map[string]any{
		"withdraw_no": no, "party_id": partyID, "party_code": "approve", "amount_cents": 20000, "status": "pending",
	}).Error; err != nil {
		t.Fatal(err)
	}
	// 可用余额视图已经把这条 pending 记录抵扣掉了。
	if available, err := withdrawStore.AvailableCents(ctx, nil, partyID); err != nil || available != 10000 {
		t.Fatalf("available after reservation = %d (%v), want 10000", available, err)
	}
	// 审批余额检查必须排除当前提现单。
	var row struct {
		ID uint64 `gorm:"column:id"`
	}
	billingDB.Table("withdraw_request").Where("withdraw_no = ?", no).Take(&row)
	available, err := withdrawStore.AvailableCentsExcluding(ctx, nil, partyID, row.ID)
	if err != nil || available != 30000 {
		t.Fatalf("available excluding self = %d (%v), want 30000", available, err)
	}
	if err := billingDB.Table("withdraw_request").Where("withdraw_no = ?", no).
		Updates(map[string]any{"status": "approved", "reviewed_by": 1, "reviewed_at": "2026-09-29 00:00:00"}).Error; err != nil {
		t.Fatal(err)
	}
	// 打款同样要排除自己，而且重放时保持幂等。
	if err := billingDB.Table("withdraw_request").Where("withdraw_no = ? AND status = 'approved'", no).
		Update("status", "paid").Error; err != nil {
		t.Fatal(err)
	}
	first := billingDB.Table("withdraw_request").Where("withdraw_no = ?", no)
	firstCount := int64(0)
	first.Count(&firstCount)
	if firstCount != 1 {
		t.Fatalf("withdrawal rows = %d, want 1", firstCount)
	}
	// 重放一次打款不能再多出一行记录。
	if err := billingDB.Table("withdraw_request").Where("withdraw_no = ? AND status = 'paid'", no).
		Updates(map[string]any{"status": "paid"}).Error; err != nil {
		t.Fatal(err)
	}
	var paid int64
	billingDB.Table("withdraw_request").Where("withdraw_no = ? AND status = 'paid'", no).Count(&paid)
	if paid != 1 {
		t.Fatalf("paid rows after replay = %d", paid)
	}
}

// split_party 没有 deleted_at 列，"退休"这件事记在父模板上。
// 一条过时的查询会表现为每次提现请求都返回 503。
func TestSplitPartyLookupUsesExistingColumnsOnly(t *testing.T) {
	if os.Getenv("TEST_BILLING_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	partyID := settledParty(t, ctx, "columns", 10000, 10000)
	adminDB := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	var party struct {
		Code       string  `gorm:"column:party_code"`
		BankAcc    *string `gorm:"column:bank_account"`
		TemplateID uint64  `gorm:"column:split_template_id"`
	}
	if err := adminDB.Table("split_party").
		Select("party_code, bank_account, split_template_id").Where("id = ?", partyID).Take(&party).Error; err != nil {
		t.Fatalf("split_party lookup: %v", err)
	}
	if party.Code == "" || party.TemplateID == 0 || party.BankAcc == nil {
		t.Fatalf("party not fully resolved: %+v", party)
	}
	// 余额查询 join settlement 时不带分区条件，
	// 因为 settlement_party_amount 上没有 created_month 这一列。
	billingDB := openFinanceDB(t, "TEST_BILLING_DATABASE_URL")
	withdrawStore := settlement.WithdrawStore{DB: billingDB}
	available, err := withdrawStore.AvailableCents(ctx, nil, partyID)
	if err != nil {
		t.Fatalf("AvailableCents: %v", err)
	}
	if available != 10000 {
		t.Fatalf("available = %d, want 10000", available)
	}
}
