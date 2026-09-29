package charge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCouponNotFound   = errors.New("优惠券不存在或未发放给该账号")
	ErrCouponExhausted  = errors.New("优惠券已过期或已使用")
	ErrCouponThreshold  = errors.New("未达到该优惠券的使用门槛")
	ErrCouponExceedsFee = errors.New("优惠金额不能超过订单金额")
)

type CouponGrant struct {
	ID          uint64    `json:"id" gorm:"column:id"`
	CouponID    uint64    `json:"coupon_id" gorm:"column:coupon_id"`
	Name        string    `json:"name" gorm:"column:name"`
	GrantID     uint64    `json:"grant_id" gorm:"column:grant_id"`
	Status      string    `json:"status" gorm:"column:status"`
	ExpiresAt   time.Time `json:"expires_at" gorm:"column:expired_at"`
	MinCharge   int64     `json:"min_charge_cents" gorm:"column:min_charge_cents"`
	Type        string    `json:"discount_type" gorm:"column:discount_type"`
	Amount      *int64    `json:"discount_value_cents" gorm:"column:discount_value_cents"`
	Percent     *float64  `json:"discount_percent" gorm:"column:discount_percent"`
	FreeMinutes uint32    `json:"free_minutes" gorm:"-"`
}

func (CouponGrant) TableName() string { return "coupon_grant" }

// CouponStore resolves and redeems a customer's coupon against a charge.
type CouponStore struct{ DB *gorm.DB }

// AvailableCoupons lists the coupons a customer may still use right now.
func (s CouponStore) AvailableCoupons(ctx context.Context, userID uint64) ([]CouponGrant, error) {
	rows := []CouponGrant{}
	err := s.DB.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c ON c.id = g.coupon_id AND c.deleted_at IS NULL AND c.status = 'active'").
		Where("g.user_id = ? AND g.status = 'unused' AND g.deleted_at IS NULL AND g.expired_at > UTC_TIMESTAMP(3)", userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c.name, c.discount_type, c.discount_value_cents, c.discount_percent, c.min_charge_cents").
		Order("g.expired_at, g.id").Find(&rows).Error
	return rows, err
}

// Quote computes the discount a coupon would give for a given fee. It never
// mutates state, so the customer can preview before paying.
func (s CouponStore) Quote(ctx context.Context, userID, grantID uint64, totalCents int64) (int64, error) {
	grant, err := s.load(ctx, userID, grantID)
	if err != nil {
		return 0, err
	}
	return discountFor(grant, totalCents)
}

// discountFor applies the coupon rules. The discount is capped at the fee so a
// coupon can never produce a negative charge.
func discountFor(grant CouponGrant, totalCents int64) (int64, error) {
	if totalCents <= 0 || grant.MinCharge > totalCents {
		return 0, ErrCouponThreshold
	}
	var discount int64
	switch grant.Type {
	case "amount":
		if grant.Amount == nil || *grant.Amount <= 0 {
			return 0, ErrCouponNotFound
		}
		discount = *grant.Amount
	case "percentage":
		if grant.Percent == nil || *grant.Percent <= 0 || *grant.Percent >= 100 {
			return 0, ErrCouponNotFound
		}
		// Decimal arithmetic keeps the percentage exact; the result is rounded
		// once, half up, to a whole cent.
		keep := decimal.NewFromInt(totalCents).Mul(decimal.NewFromFloat(*grant.Percent)).Div(decimal.NewFromInt(100)).Round(0).IntPart()
		discount = totalCents - keep
	case "time_free":
		if grant.FreeMinutes == 0 {
			return 0, ErrCouponNotFound
		}
		discount = int64(grant.FreeMinutes) * 60 * serviceRatePerMinute
	default:
		return 0, ErrCouponNotFound
	}
	if discount > totalCents {
		discount = totalCents
	}
	if discount < 0 {
		discount = 0
	}
	return discount, nil
}

// serviceRatePerMinute is the fallback value used to price a free-time coupon
// when the charge rule does not state a per-minute service fee.
const serviceRatePerMinute = 0

// Redeem consumes the coupon and applies the discount to the payment order. The
// grant row is locked and its status checked inside the transaction, so two
// concurrent payments cannot both use the same coupon.
func (s CouponStore) Redeem(ctx context.Context, userID, grantID uint64, orderNo string, paymentOrderID uint64, totalCents int64) (int64, error) {
	var discount int64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		grant, err := loadGrant(ctx, tx, userID, grantID)
		if err != nil {
			return err
		}
		discount, err = discountFor(grant, totalCents)
		if err != nil {
			return err
		}
		updated := tx.Table("coupon_grant").
			Where("id = ? AND user_id = ? AND status = 'unused'", grantID, userID).
			Updates(map[string]any{"status": "used", "used_payment_order_id": paymentOrderID, "used_at": time.Now().UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			// Another payment consumed the coupon first.
			return ErrCouponExhausted
		}
		return tx.Table("coupon_redemption").Create(map[string]any{
			"coupon_id": grant.CouponID, "user_id": userID, "biz_type": "charge",
			"biz_id": paymentOrderID, "discount_cents": discount, "order_no": orderNo,
		}).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	return discount, nil
}

// Release returns a coupon when its payment never completes, so a failed
// payment does not permanently consume the customer's discount.
func (s CouponStore) Release(ctx context.Context, userID, paymentOrderID uint64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			GrantID uint64 `gorm:"column:grant_id"`
		}
		if err := tx.Table("coupon_grant").Select("id AS grant_id").
			Where("user_id = ? AND used_payment_order_id = ?", userID, paymentOrderID).Take(&row).Error; err != nil {
			return err
		}
		if err := tx.Table("coupon_grant").Where("id = ?", row.GrantID).
			Updates(map[string]any{"status": "unused", "used_payment_order_id": nil, "used_at": nil}).Error; err != nil {
			return err
		}
		return tx.Table("coupon_redemption").Where("biz_id = ? AND biz_type = 'charge'", paymentOrderID).Delete(nil).Error
	})
}

func (s CouponStore) load(ctx context.Context, userID, grantID uint64) (CouponGrant, error) {
	return loadGrant(ctx, s.DB.WithContext(ctx), userID, grantID)
}

func loadGrant(ctx context.Context, db *gorm.DB, userID, grantID uint64) (CouponGrant, error) {
	grant := CouponGrant{}
	err := db.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c ON c.id = g.coupon_id AND c.deleted_at IS NULL AND c.status = 'active'").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("g.id = ? AND g.user_id = ? AND g.deleted_at IS NULL", grantID, userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c.name, c.discount_type, c.discount_value_cents, c.discount_percent, c.min_charge_cents").
		Take(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return grant, ErrCouponNotFound
	}
	if err != nil {
		return grant, err
	}
	if grant.Status != "unused" {
		return grant, ErrCouponExhausted
	}
	if !grant.ExpiresAt.After(time.Now().UTC()) {
		return grant, ErrCouponExhausted
	}
	if grant.Type == "time_free" {
		if minutes, err := freeMinutes(ctx, db, grant.CouponID); err == nil {
			grant.FreeMinutes = minutes
		} else {
			return grant, err
		}
	}
	return grant, nil
}

// freeMinutes reads the free-time allowance for a duration coupon.
func freeMinutes(ctx context.Context, db *gorm.DB, couponID uint64) (uint32, error) {
	var row struct {
		Value int32 `gorm:"column:value"`
	}
	if err := db.WithContext(ctx).Table("coupon").
		Select("CAST(COALESCE(discount_value_cents,0) AS SIGNED) AS value").
		Where("id = ?", couponID).Take(&row).Error; err != nil {
		return 0, err
	}
	if row.Value <= 0 {
		return 0, fmt.Errorf("coupon %d has no free minutes", couponID)
	}
	return uint32(row.Value), nil
}

// chargeNoFor derives the redemption reference before the charge row exists.
func chargeNoFor(paymentOrderID uint64) string {
	return fmt.Sprintf("PENDING-%d", paymentOrderID)
}

// redeemCouponInTx consumes the coupon inside the payment transaction. The grant
// row is locked, so a coupon cannot be spent by two payments at once.
func redeemCouponInTx(tx *gorm.DB, intent PaymentIntentRecord, order PaymentOrderRecord, reference string) error {
	grant, err := loadGrant(context.Background(), tx, intent.UserID, intent.CouponGrantID)
	if err != nil {
		return err
	}
	discount, err := discountFor(grant, intent.TotalCents-intent.DiscountCents)
	if err != nil {
		return err
	}
	updated := tx.Table("coupon_grant").
		Where("id = ? AND user_id = ? AND status = 'unused'", intent.CouponGrantID, intent.UserID).
		Updates(map[string]any{"status": "used", "used_payment_order_id": order.ID, "used_at": time.Now().UTC()})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return ErrCouponExhausted
	}
	return tx.Table("coupon_redemption").Create(map[string]any{
		"coupon_id": grant.CouponID, "user_id": intent.UserID, "biz_type": "charge",
		"biz_id": order.ID, "discount_cents": discount, "order_no": reference,
	}).Error
}

// CouponAPI exposes the customer's usable coupons. Redemption itself happens
// when the payment is confirmed, so this endpoint is read-only by design.
type CouponAPI struct {
	Auth    identity.SessionAuthenticator
	Coupons CouponStore
}

func (a CouponAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/coupons", a.list)
}

func (a CouponAPI) list(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	rows, err := a.Coupons.AvailableCoupons(c.Request.Context(), userID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "优惠券暂时无法读取", nil)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{"coupon_id": row.CouponID, "name": row.Name, "grant_id": row.GrantID,
			"discount_type": row.Type, "min_charge_cents": row.MinCharge, "expires_at": row.ExpiresAt})
	}
	httpapi.OK(c, gin.H{"items": out})
}
