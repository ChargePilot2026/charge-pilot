package admin

import (
	"errors"
	"regexp"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type couponRow struct {
	ID                 uint64           `json:"id"`
	Code               string           `json:"code"`
	Name               string           `json:"name"`
	DiscountType       string           `json:"discount_type"`
	DiscountValueCents *int64           `json:"discount_value_cents"`
	DiscountPercent    *decimal.Decimal `json:"discount_percent"`
	FreeMinutes        *uint32          `json:"free_minutes"`
	MinChargeCents     int64            `json:"min_charge_cents"`
	ValidHours         uint32           `json:"valid_hours"`
	TotalQuota         uint32           `json:"total_quota"`
	PerUserQuota       uint32           `json:"per_user_quota"`
	Status             string           `json:"status"`
	StartAt            *time.Time       `json:"start_at"`
	EndAt              *time.Time       `json:"end_at"`
}

func (a ResourceAPI) registerCoupons(r *gin.Engine) {
	r.GET("/api/v1/admin/coupons", a.Auth.Require("coupon.read"), a.coupons)
	r.POST("/api/v1/admin/coupons", a.Auth.Require("coupon.create"), a.createCoupon)
	r.PUT("/api/v1/admin/coupons/:id", a.Auth.Require("coupon.update"), a.updateCoupon)
	r.GET("/api/v1/admin/coupons/:id/stats", a.Auth.Require("coupon.read"), a.couponStats)
	r.POST("/api/v1/admin/coupons/:id/grants", a.Auth.Require("coupon.grant"), a.grantCoupon)
}
func (a ResourceAPI) coupons(c *gin.Context) {
	rows := []couponRow{}
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("coupon").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}
func (a ResourceAPI) createCoupon(c *gin.Context) {
	var in couponRow
	if !decodeResource(c, &in) {
		return
	}
	if in.ID != 0 || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(in.Code) || !validText(in.Name, 128) || in.MinChargeCents < 0 || in.ValidHours == 0 || in.ValidHours > 87600 || in.PerUserQuota == 0 || (in.EndAt != nil && in.StartAt != nil && !in.EndAt.After(*in.StartAt)) {
		httpapi.BadRequest(c, "优惠券名称、编码、有效期或额度无效")
		return
	}
	valid := false
	switch in.DiscountType {
	case "amount":
		valid = in.DiscountValueCents != nil && *in.DiscountValueCents > 0 && in.DiscountPercent == nil && in.FreeMinutes == nil
	case "percentage":
		valid = in.DiscountPercent != nil && in.DiscountPercent.GreaterThan(decimal.Zero) && in.DiscountPercent.LessThan(decimal.NewFromInt(100)) && in.DiscountPercent.Equal(in.DiscountPercent.Round(2)) && in.DiscountValueCents == nil && in.FreeMinutes == nil
	case "time_free":
		valid = in.FreeMinutes != nil && *in.FreeMinutes > 0 && *in.FreeMinutes <= 1440 && in.DiscountPercent == nil && in.DiscountValueCents == nil
	}
	if !valid {
		httpapi.BadRequest(c, "优惠参数无效：满减分值须大于 0，折扣为 0–100 之间，免费时长为 1–1440 分钟")
		return
	}
	in.Status = "active"
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("coupon").Create(&in).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "create", "coupon", in.ID, nil, in, c.ClientIP(), "")
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, in)
}
func (a ResourceAPI) updateCoupon(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Name, 128) || (in.Status != "active" && in.Status != "disabled") {
		httpapi.BadRequest(c, "名称或状态无效")
		return
	}
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before couponRow
		if err := tx.Table("coupon").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("coupon").Where("id=?", id).Updates(map[string]any{"name": in.Name, "status": in.Status}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "update", "coupon", id, before, in, c.ClientIP(), "")
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, in)
}
func (a ResourceAPI) couponStats(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var coupon couponRow
	db := a.Store.UserDB.WithContext(c.Request.Context())
	if err := db.Table("coupon").Where("id=? AND deleted_at IS NULL", id).Take(&coupon).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	var stats struct {
		GrantedCount int64 `json:"granted_count"`
		UnusedCount  int64 `json:"unused_count"`
		UsedCount    int64 `json:"used_count"`
		ExpiredCount int64 `json:"expired_count"`
	}
	if err := db.Table("coupon_grant").Select("COUNT(*) AS granted_count,COALESCE(SUM(status='unused' AND expired_at>UTC_TIMESTAMP(3)),0) AS unused_count,COALESCE(SUM(status='used'),0) AS used_count,COALESCE(SUM(status='expired' OR (status='unused' AND expired_at<=UTC_TIMESTAMP(3))),0) AS expired_count").Where("coupon_id=?", id).Scan(&stats).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rate := float64(0)
	if stats.GrantedCount > 0 {
		rate = float64(stats.UsedCount) / float64(stats.GrantedCount)
	}
	httpapi.OK(c, gin.H{"total_quota": coupon.TotalQuota, "granted_count": stats.GrantedCount, "unused_count": stats.UnusedCount, "used_count": stats.UsedCount, "expired_count": stats.ExpiredCount, "usage_rate": rate})
}

var requestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (a ResourceAPI) grantCoupon(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID string `json:"request_id"`
		UserID    uint64 `json:"user_id"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !requestIDPattern.MatchString(in.RequestID) || in.UserID == 0 {
		httpapi.BadRequest(c, "请求编号或用户编号无效")
		return
	}
	var grantID uint64
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var coupon couponRow
		if err := tx.Table("coupon").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&coupon).Error; err != nil {
			return err
		}
		var receipt struct{ CouponID, UserID, CouponGrantID uint64 }
		err := tx.Table("coupon_grant_request").Where("request_id=?", in.RequestID).Take(&receipt).Error
		if err == nil {
			if receipt.CouponID != id || receipt.UserID != in.UserID {
				return errConflict
			}
			grantID = receipt.CouponGrantID
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().UTC()
		if coupon.Status != "active" || (coupon.StartAt != nil && now.Before(*coupon.StartAt)) || (coupon.EndAt != nil && !now.Before(*coupon.EndAt)) {
			return errConflict
		}
		var user struct{ Status string }
		if err := tx.Table("user").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", in.UserID).Take(&user).Error; err != nil {
			return err
		}
		if user.Status != "active" {
			return errConflict
		}
		var total, perUser int64
		if err := tx.Table("coupon_grant").Where("coupon_id=?", id).Count(&total).Error; err != nil {
			return err
		}
		if err := tx.Table("coupon_grant").Where("coupon_id=? AND user_id=?", id, in.UserID).Count(&perUser).Error; err != nil {
			return err
		}
		if (coupon.TotalQuota > 0 && total >= int64(coupon.TotalQuota)) || perUser >= int64(coupon.PerUserQuota) {
			return errConflict
		}
		expires := now.Add(time.Duration(coupon.ValidHours) * time.Hour)
		if coupon.EndAt != nil && coupon.EndAt.Before(expires) {
			expires = *coupon.EndAt
		}
		if err := tx.Table("coupon_grant").Create(map[string]any{"coupon_id": id, "user_id": in.UserID, "grant_source": "manual", "status": "unused", "expired_at": expires, "source_event_id": in.RequestID}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&grantID).Error; err != nil {
			return err
		}
		if err := tx.Table("coupon_grant_request").Create(map[string]any{"request_id": in.RequestID, "coupon_id": id, "user_id": in.UserID, "coupon_grant_id": grantID}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "grant", "coupon", id, nil, in, c.ClientIP(), in.RequestID)
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"coupon_grant_id": grantID, "request_id": in.RequestID})
}
