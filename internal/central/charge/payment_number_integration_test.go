package charge

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/snowflake"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Exercise real source-specific receipt paths without collecting a payment or
// dispatching a gateway command. The outer transaction rolls back all fixtures,
// including the allocator state, wallet movements and card/charge outbox records.
func TestPaymentNumberSourcesAndDebtRequestReplay(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("migrated disposable user MySQL database required")
	}
	db := openAccountDB(t, "TEST_USER_DATABASE_URL")
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	ctx := context.Background()
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")
	userID, err := snowflake.Next(tx)
	if err != nil {
		t.Fatal(err)
	}
	openID := "numbering-" + tag
	if err := tx.Exec("INSERT INTO user(id,openid,status) VALUES(?,?,'active')", userID, openID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("INSERT INTO wallet_account(user_id,balance_cents,status) VALUES(?,10000,'active')", userID).Error; err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	readPayment := func(no string) PaymentOrderRecord {
		t.Helper()
		var row PaymentOrderRecord
		if err := tx.Where("order_no=? AND user_id=?", no, userID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	assertNewNumber := func(row PaymentOrderRecord) {
		t.Helper()
		if !strings.HasPrefix(row.OrderNo, "P") {
			t.Fatalf("new payment lacks P prefix: %+v", row)
		}
		id, err := strconv.ParseUint(row.OrderNo[1:], 10, 64)
		if err != nil || id == 0 || id == userID || strconv.FormatUint(id, 10) != row.OrderNo[1:] || seen[row.OrderNo] {
			t.Fatalf("payment identifier must be unique and independent of its owner: %+v err=%v", row, err)
		}
		seen[row.OrderNo] = true
	}
	port := func(no uint8) ScanResult {
		return ScanResult{Kind: "port", DeviceID: tag, StationID: 9,
			Port: &ScanPort{PortID: tag + ":" + strconv.Itoa(int(no)), DeviceID: tag, PortNo: no, Available: true, Online: true}}
	}
	intents := PaymentIntentStore{DB: tx}
	var scanPayment PaymentIntent
	for no := uint8(1); no <= 2; no++ {
		input := completeIntent(IntentInput{UserID: userID, ClientRequestID: uuid.NewString(), Port: port(no)}, 200)
		first, err := intents.Reserve(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		assertNewNumber(readPayment(first.MerchantOrderNo))
		if replay, err := intents.Reserve(ctx, input); err != nil || replay.MerchantOrderNo != first.MerchantOrderNo || replay.PaymentOrderID != first.PaymentOrderID {
			t.Fatalf("scan UUID replay changed payment: %+v %v", replay, err)
		}
		if no == 1 {
			scanPayment = first
		}
	}
	callbacks := PaymentCallbackStore{DB: tx, ExpectedProvider: "simulation", ExpectedMerchantID: "numbering-simulation", ExpectedAppID: "wx_numbering_test"}
	notification := VerifiedPayment{Provider: "simulation", MerchantID: callbacks.ExpectedMerchantID, AppID: callbacks.ExpectedAppID,
		MerchantOrderNo: scanPayment.MerchantOrderNo, TransactionID: "numbering-tx-" + tag, OpenID: openID, PaidCents: scanPayment.PayableCents, PaidAt: time.Now().UTC()}
	confirmed, err := callbacks.Apply(ctx, notification)
	if err != nil || confirmed.ChargeOrderID == 0 || confirmed.Replayed || !strings.HasPrefix(confirmed.ChargeOrderNo, "C") ||
		len(confirmed.ChargeOrderNo) != 17+len(tag) || !strings.HasSuffix(confirmed.ChargeOrderNo, tag+"01") {
		t.Fatalf("P payment did not create a C charge order: %+v %v", confirmed, err)
	}
	if _, err := time.Parse("20060102150405", confirmed.ChargeOrderNo[1:15]); err != nil {
		t.Fatalf("invalid C request timestamp: %s", confirmed.ChargeOrderNo)
	}
	if replay, err := callbacks.Apply(ctx, notification); err != nil || !replay.Replayed || replay.ChargeOrderID != confirmed.ChargeOrderID || replay.ChargeOrderNo != confirmed.ChargeOrderNo {
		t.Fatalf("verified notification replay created a different charge order: %+v %v", replay, err)
	}

	cardUUID := uuid.New()
	card := OnlineCard{CardNo: strconv.FormatUint(uint64(binary.LittleEndian.Uint32(cardUUID[:4])|1), 10), UserID: userID, Status: "active"}
	if err := tx.Create(&card).Error; err != nil {
		t.Fatal(err)
	}
	scheme := pricing.Scheme{Name: "payment number card fixture", Packages: []pricing.Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 200, Minutes: 120}},
		Card: pricing.CardPolicy{PackageID: 1, MaxMinutes: 600}, Display: pricing.DefaultDisplay()}.Normalized()
	rule := pricing.Rule{ID: 31, StationID: 9, Version: 1, Spec: scheme.SpecFor(scheme.Packages[0])}
	cards := CardStore{DB: tx}
	var cardOrders []ChargeOrderRecord
	for no := uint8(3); no <= 4; no++ {
		event := uuid.NewString()
		first, err := cards.Swipe(ctx, card.CardNo, event, port(no), rule)
		if err != nil {
			t.Fatal(err)
		}
		var order ChargeOrderRecord
		if err := tx.Where("id=? AND user_id=?", first.ChargeOrderID, userID).Take(&order).Error; err != nil {
			t.Fatal(err)
		}
		var paid PaymentOrderRecord
		if err := tx.Where("id=?", order.PaymentOrderID.Int64).Take(&paid).Error; err != nil || paid.PayMethod != "balance" {
			t.Fatalf("card payment: %+v %v", paid, err)
		}
		assertNewNumber(paid)
		if replay, err := cards.Swipe(ctx, card.CardNo, event, port(no), pricing.Rule{}); err != nil || replay.OperationID != first.OperationID || replay.ChargeOrderID != first.ChargeOrderID {
			t.Fatalf("card event replay changed payment: %+v %v", replay, err)
		}
		cardOrders = append(cardOrders, order)
	}

	jwt, err := auth.NewJWT("numbering-test-secret-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(userID, 10), Kind: "user", OpenID: openID, SessionID: "numbering-test",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	UserAccountAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: scanSession{}, Users: identity.UserStore{DB: tx}}, UserDB: tx, Prepay: payment.Simulator{}}.Register(router)
	requests := &authenticatedRouter{Engine: router, token: token}
	for range 2 {
		request := map[string]any{"request_id": uuid.NewString(), "amount_cents": 1000}
		status, body := callJSON(t, requests, http.MethodPost, "/api/v1/user/wallet/recharge", request)
		if status != http.StatusOK {
			t.Fatalf("recharge: %d %v", status, body)
		}
		data := body["data"].(map[string]any)
		assertNewNumber(readPayment(data["merchant_order_no"].(string)))
		status, body = callJSON(t, requests, http.MethodPost, "/api/v1/user/wallet/recharge", request)
		if status != http.StatusOK || body["data"].(map[string]any)["merchant_order_no"] != data["merchant_order_no"] || body["data"].(map[string]any)["payment_order_id"] != data["payment_order_id"] {
			t.Fatalf("recharge UUID replay changed payment: %d %v", status, body)
		}
	}

	debts := DebtStore{DB: tx}
	var debtRows []Debt
	for i, order := range cardOrders {
		debt := Debt{DebtNo: "number-debt-" + tag + strconv.Itoa(i), ChargeOrderID: order.ID, PaymentOrderID: uint64(order.PaymentOrderID.Int64),
			UserID: userID, DebtCents: 50, Status: "unpaid"}
		if err := tx.Create(&debt).Error; err != nil {
			t.Fatal(err)
		}
		debtRows = append(debtRows, debt)
	}
	for range 2 {
		requestID := uuid.NewString()
		id, no, amount, payer, err := debts.OpenDebtPayment(ctx, debtRows[0].ID, requestID)
		if err != nil || amount != 50 || payer != openID {
			t.Fatalf("new debt payment: %d %s %d %s %v", id, no, amount, payer, err)
		}
		assertNewNumber(readPayment(no))
		if retryID, retryNo, _, _, err := debts.OpenDebtPayment(ctx, debtRows[0].ID, requestID); err != nil || retryID != id || retryNo != no {
			t.Fatalf("debt UUID replay changed payment: %d %s %v", retryID, retryNo, err)
		}
		if _, _, _, _, err := debts.OpenDebtPayment(ctx, debtRows[1].ID, requestID); !errors.Is(err, ErrPaymentIntentConflict) {
			t.Fatalf("request UUID reused for another debt: %v", err)
		}
	}
	legacyRequest := uuid.NewString()
	legacy := PaymentOrderRecord{OrderNo: "DPAY" + legacyRequest, BizType: "charge_debt", BizID: debtRows[0].ID, UserID: userID,
		PayMethod: "wechat", TotalCents: 50, Status: "initiated", CreatedMonth: utcDate()}
	if err := tx.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if id, no, _, _, err := debts.OpenDebtPayment(ctx, debtRows[0].ID, legacyRequest); err != nil || id != legacy.ID || no != legacy.OrderNo {
		t.Fatalf("legacy DPAY replay changed payment: %d %s %v", id, no, err)
	}
	if _, _, _, _, err := debts.OpenDebtPayment(ctx, debtRows[1].ID, legacyRequest); !errors.Is(err, ErrPaymentIntentConflict) {
		t.Fatalf("legacy UUID reused for another debt: %v", err)
	}
	var payments, charges, receipts, walletTxns int64
	if err := tx.Model(&PaymentOrderRecord{}).Where("user_id=?", userID).Count(&payments).Error; err != nil || payments != 9 {
		t.Fatalf("replays created extra payments: %d %v", payments, err)
	}
	if err := tx.Table("charge_debt_payment_request").Where("debt_id=?", debtRows[0].ID).Count(&receipts).Error; err != nil || receipts != 2 {
		t.Fatalf("new debt request receipts: %d %v", receipts, err)
	}
	if err := tx.Table("wallet_txn").Where("user_id=?", userID).Count(&walletTxns).Error; err != nil || walletTxns != 2 {
		t.Fatalf("card replay debited wallet twice: %d %v", walletTxns, err)
	}
	if err := tx.Model(&ChargeOrderRecord{}).Where("user_id=?", userID).Count(&charges).Error; err != nil || charges != 3 {
		t.Fatalf("notification or card replay created extra charge orders: %d %v", charges, err)
	}
}
