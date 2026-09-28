package charge

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestPaymentIntentHoldsPortWithoutCreatingChargeOrder(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable user database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "intent-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	userID, err := user.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	defer db.ExecContext(ctx, "DELETE FROM payment_order WHERE user_id = ?", userID)
	defer db.ExecContext(ctx, "DELETE FROM charge_payment_intent WHERE user_id = ?", userID)
	portCode := "intent-device:1"
	input := IntentInput{UserID: uint64(userID), ClientRequestID: uuid.NewString(),
		Port: ScanResult{Kind: "port", DeviceID: "intent-device", StationID: 9,
			Port: &ScanPort{PortID: portCode, DeviceID: "intent-device", PortNo: 1, Online: true, Available: true}},
		Energy: "1", Minutes: 60,
		Rule: pricing.Rule{ID: 3, StationID: 9, Version: 1, Mode: "kwh", ServiceCentsPerKWh: 40,
			Periods: []pricing.Period{{Start: "00:00", End: "24:00", ElectricPriceCents: 100}}}}
	store := PaymentIntentStore{DB: testGORMDB(t, db)}
	first, err := store.Reserve(ctx, input)
	if err != nil || first.PaymentOrderID == 0 || first.Estimate.TotalCents != 140 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := store.Reserve(ctx, input)
	if err != nil || second.IntentID != first.IntentID {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	input.ClientRequestID = uuid.NewString()
	if _, err := store.Reserve(ctx, input); !errors.Is(err, ErrPaymentIntentConflict) {
		t.Fatalf("active port conflict: %v", err)
	}
	var charges, payments, intents int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_order WHERE user_id = ?", userID).Scan(&charges); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM payment_order WHERE user_id = ?", userID).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_payment_intent WHERE user_id = ?", userID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if charges != 0 || payments != 1 || intents != 1 {
		t.Fatalf("charge_orders=%d payment_orders=%d intents=%d", charges, payments, intents)
	}
}
