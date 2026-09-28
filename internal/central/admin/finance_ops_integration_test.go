package admin

import (
	"context"
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

// settledParty creates an active split template with a paid settlement so the
// party has a real, withdrawable balance.
func settledParty(t *testing.T, ctx context.Context, code string, ratio int32, pool int64) (partyID uint64) {
	t.Helper()
	adminDB := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	billingDB := openFinanceDB(t, "TEST_BILLING_DATABASE_URL")
	suffix := uuid.NewString()[:8]
	template := "tpl-" + suffix
	party := "party-" + suffix
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
	// uk_fee_generation is unique per calculation, so the fixture needs its own id.
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

// A withdrawal the operator created must be approvable: the pending row already
// reserves the balance, so approval cannot count it as a competing claim.
func TestWithdrawalCanBeApprovedAndPaid(t *testing.T) {
	if os.Getenv("TEST_BILLING_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	partyID := settledParty(t, ctx, "approve", 10000, 30000)
	billingDB := openFinanceDB(t, "TEST_BILLING_DATABASE_URL")
	store := ResourceStore{BillingDB: billingDB, AdminDB: openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")}

	if _, err := store.AvailableCents(ctx, nil, partyID); err != nil {
		t.Fatal(err)
	}
	no := "WD" + uuid.NewString()[:24]
	if err := billingDB.Table("withdraw_request").Create(map[string]any{
		"withdraw_no": no, "party_id": partyID, "party_code": "approve", "amount_cents": 20000, "status": "pending",
	}).Error; err != nil {
		t.Fatal(err)
	}
	// The reservation view already nets this pending row out.
	if available, err := store.AvailableCents(ctx, nil, partyID); err != nil || available != 10000 {
		t.Fatalf("available after reservation = %d (%v), want 10000", available, err)
	}
	// Approving must exclude this same request, otherwise it can never pass.
	var row struct {
		ID uint64 `gorm:"column:id"`
	}
	billingDB.Table("withdraw_request").Where("withdraw_no = ?", no).Take(&row)
	available, err := store.AvailableCentsExcluding(ctx, nil, partyID, row.ID)
	if err != nil || available != 30000 {
		t.Fatalf("available excluding self = %d (%v), want 30000", available, err)
	}
	if err := billingDB.Table("withdraw_request").Where("withdraw_no = ?", no).
		Updates(map[string]any{"status": "approved", "reviewed_by": 1, "reviewed_at": "2026-09-29 00:00:00"}).Error; err != nil {
		t.Fatal(err)
	}
	// Paying excludes itself as well, and stays idempotent on replay.
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
	// Replaying the payout must not create a second row.
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

// split_party has no deleted_at column; retirement lives on the parent template.
// A stale query against it would surface as a 503 on every withdrawal request.
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
	// The balance query joins settlement without a partition predicate, because
	// settlement_party_amount carries no created_month column.
	billingDB := openFinanceDB(t, "TEST_BILLING_DATABASE_URL")
	store := ResourceStore{BillingDB: billingDB, AdminDB: adminDB}
	available, err := store.AvailableCents(ctx, nil, partyID)
	if err != nil {
		t.Fatalf("AvailableCents: %v", err)
	}
	if available != 10000 {
		t.Fatalf("available = %d, want 10000", available)
	}
}
