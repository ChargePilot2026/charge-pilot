package charge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrDebtNotFound = errors.New("欠费记录不存在")
	ErrDebtConflict = errors.New("欠费状态已变化，请刷新后重试")
)

// Debt is an amount a charge actually cost more than the customer prepaid. It
// is created by the billing job and collected later, so the charge lifecycle
// never blocks on collecting the difference.
type Debt struct {
	ID               uint64     `json:"id" gorm:"column:id"`
	DebtNo           string     `json:"debt_no" gorm:"column:debt_no"`
	ChargeOrderID    uint64     `json:"charge_order_id" gorm:"column:charge_order_id"`
	PaymentOrderID   uint64     `json:"payment_order_id" gorm:"column:payment_order_id"`
	UserID           uint64     `json:"user_id" gorm:"column:user_id"`
	DebtCents        int64      `json:"debt_cents" gorm:"column:debt_cents"`
	PaidCents        int64      `json:"paid_cents" gorm:"column:paid_cents"`
	Status           string     `json:"status" gorm:"column:status"`
	OutstandingCents int64      `json:"outstanding_cents" gorm:"-"`
	CreatedAt        time.Time  `json:"created_at" gorm:"column:created_at"`
	LastReminderAt   *time.Time `json:"last_reminder_at" gorm:"column:last_reminder_at"`
}

func (Debt) TableName() string { return "charge_debt" }

// OutstandingCents recomputes what is still owed.
func (d Debt) Outstanding() int64 {
	if d.DebtCents-d.PaidCents < 0 {
		return 0
	}
	return d.DebtCents - d.PaidCents
}

// DebtStore owns the shortfall lifecycle in user_db, alongside the charge and
// payment records it references.
type DebtStore struct{ DB *gorm.DB }

// RecordDebt persists or confirms the shortfall recorded on a fee receipt.
// Replaying billing must never produce a second debt for the same order.
func (s DebtStore) RecordDebt(ctx context.Context, chargeOrderID uint64, shortfallCents int64) error {
	if shortfallCents <= 0 {
		return nil
	}
	var receipt struct {
		PaymentOrderID uint64 `gorm:"column:payment_order_id"`
	}
	if err := s.DB.WithContext(ctx).Table("charge_fee_receipt AS f").
		Joins("JOIN charge_order AS c ON c.id = f.charge_order_id").
		Select("c.payment_order_id").Where("f.charge_order_id = ?", chargeOrderID).Take(&receipt).Error; err != nil {
		return err
	}
	if receipt.PaymentOrderID == 0 {
		return ErrDebtConflict
	}
	var user struct {
		UserID uint64 `gorm:"column:user_id"`
	}
	if err := s.DB.WithContext(ctx).Table("charge_order").Select("user_id").Where("id = ?", chargeOrderID).Take(&user).Error; err != nil {
		return err
	}
	debtNo := fmt.Sprintf("DEBT%020d", chargeOrderID)
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing struct {
			ID        uint64 `gorm:"column:id"`
			DebtCents int64  `gorm:"column:debt_cents"`
		}
		found := tx.Table("charge_debt").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id = ?", chargeOrderID).Take(&existing)
		if found.Error == nil {
			if existing.DebtCents != shortfallCents {
				return ErrDebtConflict
			}
			return nil
		}
		if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		if err := tx.Table("charge_debt").Create(map[string]any{
			"debt_no": debtNo, "charge_order_id": chargeOrderID, "payment_order_id": receipt.PaymentOrderID,
			"user_id": user.UserID, "debt_cents": shortfallCents, "paid_cents": 0, "status": "unpaid",
		}).Error; err != nil {
			return err
		}
		return nil
	})
}

// ListDebts returns the debts an account still owes, newest first.
func (s DebtStore) ListDebts(ctx context.Context, userID uint64, status string, page, pageSize int) ([]Debt, int64, error) {
	query := s.DB.WithContext(ctx).Table("charge_debt").Where("user_id = ?", userID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []Debt{}
	if err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	for i := range rows {
		rows[i].OutstandingCents = rows[i].Outstanding()
	}
	return rows, total, nil
}

// OpenDebtPayment creates the payment order used to collect a debt. The amount
// is exactly the outstanding balance, so a stale page cannot overcharge.
func (s DebtStore) OpenDebtPayment(ctx context.Context, debtID uint64, clientRequestID string) (uint64, string, int64, string, error) {
	if clientRequestID == "" || uuid.Validate(clientRequestID) != nil {
		return 0, "", 0, "", ErrPaymentIntentConflict
	}
	var paymentOrderID uint64
	var amountCents int64
	var openID string
	orderNo := "DPAY" + clientRequestID
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing struct {
			ID         uint64 `gorm:"column:id"`
			UserID     uint64 `gorm:"column:user_id"`
			TotalCents int64  `gorm:"column:total_cents"`
		}
		found := tx.Table("payment_order").Where("order_no = ?", orderNo).Take(&existing)
		if found.Error == nil {
			paymentOrderID = existing.ID
			amountCents = existing.TotalCents
			return s.loadPayer(tx, existing.UserID, &openID)
		}
		if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		var debt Debt
		if err := tx.Table("charge_debt").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", debtID).Take(&debt).Error; err != nil {
			return err
		}
		outstanding := debt.Outstanding()
		if outstanding <= 0 || debt.Status == "settled" || debt.Status == "waived" {
			return ErrDebtConflict
		}
		if err := s.loadPayer(tx, debt.UserID, &openID); err != nil {
			return err
		}
		amountCents = outstanding
		if err := tx.Table("payment_order").Create(map[string]any{
			"order_no": orderNo, "biz_type": "charge_debt", "biz_id": debt.ID, "user_id": debt.UserID,
			"pay_method": "wechat", "total_cents": outstanding, "paid_cents": 0, "status": "pending",
			"created_month": utcDate(),
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&paymentOrderID).Error; err != nil {
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, "", 0, "", err
	}
	return paymentOrderID, orderNo, amountCents, openID, nil
}

// loadPayer reads the openid the channel needs to start a payment.
func (s DebtStore) loadPayer(tx *gorm.DB, userID uint64, openID *string) error {
	var payer struct {
		OpenID string `gorm:"column:openid"`
	}
	if err := tx.Table("user").Select("openid").Where("id = ? AND deleted_at IS NULL", userID).Take(&payer).Error; err != nil {
		return err
	}
	if payer.OpenID == "" {
		return ErrPaymentIntentConflict
	}
	*openID = payer.OpenID
	return nil
}

// SavePrepay caches the channel parameters so a retried request reuses the same
// prepay id instead of creating a second one at the channel.
func (s DebtStore) SavePrepay(ctx context.Context, paymentOrderID uint64, params payment.PrepayParams) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	// charge_prepay is keyed by payment order, so a resend replaces the snapshot.
	return s.DB.WithContext(ctx).Where("payment_order_id = ?", paymentOrderID).
		Clauses(clause.OnConflict{UpdateAll: true}).
		Create(map[string]any{"payment_order_id": paymentOrderID, "params_json": string(encoded)}).Error
}

// SettleDebt applies a verified channel receipt to a debt. The unique keys on
// charge_debt_receipt make a duplicated callback a no-op rather than a double
// credit, and the debt only closes once it is fully covered.
func (s DebtStore) SettleDebt(ctx context.Context, paymentOrderID uint64, paidCents int64, channelRef string) (bool, error) {
	if paidCents <= 0 || channelRef == "" {
		return false, ErrDebtConflict
	}
	applied := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var payment struct {
			ID        uint64 `gorm:"column:id"`
			BizType   string `gorm:"column:biz_type"`
			BizID     uint64 `gorm:"column:biz_id"`
			UserID    uint64 `gorm:"column:user_id"`
			Status    string `gorm:"column:status"`
			PaidCents int64  `gorm:"column:paid_cents"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", paymentOrderID).Take(&payment).Error; err != nil {
			return err
		}
		if payment.BizType != "charge_debt" || payment.Status != "paid" {
			return ErrDebtConflict
		}
		if err := tx.Table("charge_debt_receipt").Create(map[string]any{
			"debt_id": payment.BizID, "payment_order_id": paymentOrderID, "paid_cents": paidCents, "channel_ref": channelRef,
		}).Error; err != nil {
			// A duplicate channel reference or payment means this receipt was
			// already applied; treat it as success without crediting again.
			if isDuplicate(err) {
				return nil
			}
			return err
		}
		applied = true
		var debt Debt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", payment.BizID).Take(&debt).Error; err != nil {
			return err
		}
		paid := debt.PaidCents + paidCents
		status := "partial"
		if paid >= debt.DebtCents {
			paid, status = debt.DebtCents, "settled"
		}
		if err := tx.Table("charge_debt").Where("id = ?", debt.ID).Updates(map[string]any{"paid_cents": paid, "status": status}).Error; err != nil {
			return err
		}
		eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("debt-paid:"+channelRef)).String()
		return tx.Create(&ChargeEventLogRecord{ChargeOrderID: debt.ChargeOrderID, EventID: eventID, Event: "debt_paid",
			Actor: "payment", Detail: fmt.Sprintf("debt=%s paid=%d total_paid=%d status=%s", debt.DebtNo, paidCents, paid, status),
			OccurredAt: time.Now().UTC()}).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// RemindDebt records that the customer was notified, throttled by the store so
// repeated page loads cannot spam notifications.
func (s DebtStore) RemindDebt(ctx context.Context, debtID uint64) error {
	return s.DB.WithContext(ctx).Table("charge_debt").
		Where("id = ? AND status IN ('unpaid','partial') AND (last_reminder_at IS NULL OR last_reminder_at < DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 DAY))", debtID).
		Update("last_reminder_at", time.Now().UTC()).Error
}

func isDuplicate(err error) bool {
	return err != nil && (errors.Is(err, gorm.ErrDuplicatedKey) || containsDuplicate(err.Error()))
}

func containsDuplicate(message string) bool {
	return len(message) > 0 && (indexOf(message, "Duplicate entry") >= 0 || indexOf(message, "1062") >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
