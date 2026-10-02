package coupon

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

// CouponStore 负责把客户的券解析出来并核销到一笔充电上。
type CouponStore struct{ DB *gorm.DB }

// AvailableCoupons 列出客户当前还可以用的券。
func (s CouponStore) AvailableCoupons(ctx context.Context, userID uint64) ([]CouponGrant, error) {
	rows := []CouponGrant{}
	err := s.DB.WithContext(ctx).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c ON c.id = g.coupon_id AND c.deleted_at IS NULL AND c.status = 'active'").
		Where("g.user_id = ? AND g.status = 'unused' AND g.deleted_at IS NULL AND g.expired_at > UTC_TIMESTAMP(3)", userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c.name, c.discount_type, c.discount_value_cents, c.discount_percent, c.min_charge_cents").
		Order("g.expired_at, g.id").Find(&rows).Error
	return rows, err
}

// Quote 只读计算优惠券减免额，不核销券，供支付前预览。
func (s CouponStore) Quote(ctx context.Context, userID, grantID uint64, totalCents int64) (int64, error) {
	grant, err := s.load(ctx, userID, grantID)
	if err != nil {
		return 0, err
	}
	return discountFor(grant, totalCents)
}

// discountFor 应用优惠规则，减免额不超过本次费用，保证应付金额非负。
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
		// 用十进制运算保证百分比精确；
		// 结果只四舍五入一次（half up）到整分。
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

// serviceRatePerMinute 是计价时长券时的兜底值，
// 用于充电规则没有给出每分钟服务费的情况。
const serviceRatePerMinute = 0

// Redeem 在事务中锁定并校验发放记录，核销后将减免额应用到支付单，防止并发重复使用。
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
			// 券已经被另一笔支付先核销掉了。
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

// Release 在支付未完成时释放优惠券占用。
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

// freeMinutes 读取时长券的免费时长额度。
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

// RedeemRef 是支付事务传给券核销的最小事实集合，
// 避免 coupon 包反向依赖 charge 的支付记录类型。
type RedeemRef struct {
	UserID        uint64
	CouponGrantID uint64
	TotalCents    int64
	DiscountCents int64
	PaymentOrder  uint64
}

// RedeemInTx 在支付事务内核销券。
// 发放行被加锁，一张券不可能被两笔支付同时花掉。
func RedeemInTx(tx *gorm.DB, ref RedeemRef, reference string) error {
	grant, err := loadGrant(context.Background(), tx, ref.UserID, ref.CouponGrantID)
	if err != nil {
		return err
	}
	discount, err := discountFor(grant, ref.TotalCents-ref.DiscountCents)
	if err != nil {
		return err
	}
	updated := tx.Table("coupon_grant").
		Where("id = ? AND user_id = ? AND status = 'unused'", ref.CouponGrantID, ref.UserID).
		Updates(map[string]any{"status": "used", "used_payment_order_id": ref.PaymentOrder, "used_at": time.Now().UTC()})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return ErrCouponExhausted
	}
	return tx.Table("coupon_redemption").Create(map[string]any{
		"coupon_id": grant.CouponID, "user_id": ref.UserID, "biz_type": "charge",
		"biz_id": ref.PaymentOrder, "discount_cents": discount, "order_no": reference,
	}).Error
}

// CouponAPI 提供用户优惠券的只读视图；核销由支付确认流程执行。
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
