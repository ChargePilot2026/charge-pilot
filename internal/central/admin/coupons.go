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

// couponRow 是优惠券模板（user_db.coupon）的一行，列表、创建入参、审计快照共用它。
// 注意这里没有 Code 字段：券不再有业务编码，以主键 id 唯一标识
// （迁移 user_db/0037 删除了 coupon.code 与 active_code，并给 coupon_redemption 补了 coupon_id）。
// 三种优惠形式互斥，且各自的取值字段只有一个非空：
// amount 填 DiscountValueCents，percentage 填 DiscountPercent，time_free 填 FreeMinutes。
type couponRow struct {
	ID                 uint64           `json:"id"`                   // 券模板主键，也是券的唯一标识；创建时必须为 0，由数据库自增
	Name               string           `json:"name"`                 // 券名称，非空且不超过 128 字符
	DiscountType       string           `json:"discount_type"`        // 优惠形式：amount 满减 / percentage 折扣 / time_free 免时长
	DiscountValueCents *int64           `json:"discount_value_cents"` // 满减金额（分）；指针，仅 amount 模式非空且须大于 0
	DiscountPercent    *decimal.Decimal `json:"discount_percent"`     // 折扣百分比（打折数而非减免数）；指针，仅 percentage 模式非空，须大于 0 且小于 100，且最多两位小数
	FreeMinutes        *uint32          `json:"free_minutes"`         // 免单时长（分钟）；指针，仅 time_free 模式非空，取值 1–1440
	MinChargeCents     int64            `json:"min_charge_cents"`     // 使用门槛（分），订单总额须不低于此值，不可为负
	ValidHours         uint32           `json:"valid_hours"`          // 领取后的有效小时数，取值 1–87600
	TotalQuota         uint32           `json:"total_quota"`          // 发放总量上限，0 表示不限量
	PerUserQuota       uint32           `json:"per_user_quota"`       // 每人发放上限，必须大于 0
	Status             string           `json:"status"`               // 模板状态：active / disabled
	StartAt            *time.Time       `json:"start_at"`             // 生效开始时间；指针，为空表示立即生效
	EndAt              *time.Time       `json:"end_at"`               // 生效结束时间；指针，为空表示长期有效，与 StartAt 同时给出时须晚于 StartAt
}

// registerCoupons 挂载优惠券模板的查询、新建、编辑、统计与人工发放五类路由，
// 人工发放单独要求 coupon.grant 权限，因为它能直接影响用户资产。
func (a ResourceAPI) registerCoupons(r *gin.Engine) {
	r.GET("/api/v1/admin/coupons", a.Auth.Require("coupon.read"), a.coupons)
	r.POST("/api/v1/admin/coupons", a.Auth.Require("coupon.create"), a.createCoupon)
	r.PUT("/api/v1/admin/coupons/:id", a.Auth.Require("coupon.update"), a.updateCoupon)
	r.GET("/api/v1/admin/coupons/:id/stats", a.Auth.Require("coupon.read"), a.couponStats)
	r.POST("/api/v1/admin/coupons/:id/grants", a.Auth.Require("coupon.grant"), a.grantCoupon)
}

// coupons 是 GET /api/v1/admin/coupons 的处理函数：返回全部未删除的券模板，
// 按 ID 倒序，一次性返回不分页。
func (a ResourceAPI) coupons(c *gin.Context) {
	rows := []couponRow{}
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("coupon").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createCoupon 是 POST /api/v1/admin/coupons 的处理函数：新建券模板。
// 入参就是 couponRow，因此 ID 必须为 0（由库自增），没有 Code 字段需要传；
// 状态强制置为 active，不接受调用方指定。新建成功才返回，之后统一补审计。
func (a ResourceAPI) createCoupon(c *gin.Context) {
	var in couponRow
	if !decodeResource(c, &in) {
		return
	}
	if in.ID != 0 || !validText(in.Name, 128) || in.MinChargeCents < 0 || in.ValidHours == 0 || in.ValidHours > 87600 || in.PerUserQuota == 0 || (in.EndAt != nil && in.StartAt != nil && !in.EndAt.After(*in.StartAt)) {
		httpapi.BadRequest(c, "优惠券名称、有效期或额度无效")
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
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("coupon").Create(&in).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{"create", "coupon", in.ID, nil, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, in)
}

// updateCoupon 是 PUT /api/v1/admin/coupons/：id 的处理函数。
// 只能改名称和启停状态，额度与优惠力度一旦发出去就不能再改，否则已发出的券无法解释。
// 改动前对券行加写锁并留存旧值快照，审计在事务提交后另行写入。
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
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before couponRow
		if err := tx.Table("coupon").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("coupon").Where("id=?", id).Updates(map[string]any{"name": in.Name, "status": in.Status}).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{"update", "coupon", id, before, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, in)
}

// couponStats 是 GET /api/v1/admin/coupons/：id/stats 的处理函数：
// 统计该券模板已发放、未使用、已使用、已过期四种发放记录的数量及使用率。
// 未使用与已过期是按当前时刻动态判定的（status 仍是 unused 但已过期的记为过期），
// 四个状态互斥，加总等于发放总数。
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
	// 四个状态互斥，发放总数 = 未使用 + 已使用 + 已过期。
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

// requestIDPattern 要求发放请求号是标准 UUID 形态：后台人工发放可能被重复点击或重试，
// 这个号是幂等的唯一依据，格式不统一就挡不住重复发放。
var requestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// grantCoupon 是 POST /api/v1/admin/coupons/：id/grants 的处理函数：给指定用户发一张券。
// 同一个 request_id 重复提交只会返回首次发放的券 ID（幂等回执 coupon_grant_request），
// 但请求号对应的券和用户必须与本次一致，否则返回 409。
// 发券前校验券是否生效期内、用户是否正常、总量与单人额度是否用尽，全部在事务内并加行锁；
// 券的到期时间取"领取后有效期"与券自身结束时间中较早的一个。
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
	var auditPending []auditEntry
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
		auditPending = []auditEntry{{"grant", "coupon", id, nil, in, in.RequestID}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"coupon_grant_id": grantID, "request_id": in.RequestID})
}
