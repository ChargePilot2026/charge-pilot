package charge

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errActivityNotApplicable 表示这次事件没有任何规则够得着（规则过期、
// 预算用尽或并发里输掉了竞争）。调用方把它当作正常结果吞掉，
// 而不是失败——求值只汇报发了什么，其余一律略过。
var errActivityNotApplicable = errors.New("no activity rule applies")

// activityRule 是引擎真正用到的规则字段子集。
// 用结构体而不是 map 来读，schema 变更就不会悄悄改变行为。
type activityRule struct {
	ID              uint64        `gorm:"column:id"`
	TriggerType     string        `gorm:"column:trigger_type"`
	CouponID        uint64        `gorm:"column:coupon_id"`
	InviterCouponID sql.NullInt64 `gorm:"column:inviter_coupon_id"`
	ThresholdCents  int64         `gorm:"column:threshold_cents"`
	MaxGrants       int           `gorm:"column:max_grants"`
	PerUserLimit    int           `gorm:"column:per_user_limit"`
	Status          string        `gorm:"column:status"`
	StartAt         time.Time     `gorm:"column:start_at"`
	EndAt           time.Time     `gorm:"column:end_at"`
}

// activityEvent 说明发生了什么。
// EventKey 必须在重试之间保持稳定：
// 同一个现实事实必须算出同一个键，因为正是这个键让发放幂等。
type activityEvent struct {
	TriggerType string
	UserID      uint64
	// EventKey 用来区分同一类触发的多次发生，
	// 例如同一客户的两笔不同充值请求号。
	EventKey string
	// AmountCents 只有门槛类规则才会读。
	AmountCents int64
	// InviterID 只被 invite_reward 用到。
	InviterID uint64
	// Now 由外部注入，测试才不必依赖墙上时钟。
	Now time.Time
}

type activityResult struct {
	RuleID     uint64 `json:"rule_id"`
	CouponID   uint64 `json:"coupon_id"`
	InviterGot bool   `json:"inviter_rewarded"`
	Already    bool   `json:"already_granted"`
}

// ApplyActivityRules 发放客户这次事件够得着的全部券。
// 它在结算触发事实的那个事务内部被调用，
// 所以规则绝不会为一笔随后回滚的支付付出券。
//
// 券本身存放在 coupon 表里；规则只决定何时发一张券，以及这次活动最远能跑到哪一步。
// 每次发放都带着一个确定性的 source_event_id，由规则、客户和触发事实推导出来。
// 正是它让重放的事件变成空操作而不是第二张券：同一个触发永远算出同一个键，
// 而 coupon_grant_request 背后的唯一约束会把重复变成一个可以直接吞掉的重复键错误。
func ApplyActivityRules(tx *gorm.DB, event activityEvent) ([]activityResult, error) {
	if event.UserID == 0 || event.EventKey == "" || event.Now.IsZero() {
		return nil, errActivityNotApplicable
	}
	rules, err := activeRules(tx, event)
	if err != nil {
		return nil, err
	}
	results := make([]activityResult, 0, len(rules))
	for _, rule := range rules {
		result, err := grantFromRule(tx, rule, event)
		if err != nil {
			if errors.Is(err, errActivityNotApplicable) {
				continue
			}
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func activeRules(tx *gorm.DB, event activityEvent) ([]activityRule, error) {
	query := tx.Table("coupon_activity_rule").
		Where("trigger_type = ? AND status = 'active' AND deleted_at IS NULL", event.TriggerType).
		Where("start_at <= ? AND end_at > ?", event.Now, event.Now)
	if event.TriggerType == "threshold_redeem" {
		query = query.Where("threshold_cents > 0 AND threshold_cents <= ?", event.AmountCents)
	}
	var rules []activityRule
	if err := query.Order("id").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

func grantFromRule(tx *gorm.DB, rule activityRule, event activityEvent) (activityResult, error) {
	result := activityResult{RuleID: rule.ID, CouponID: rule.CouponID}

	// 客户不能邀请自己，
	// 邀请人也必须是真正用过平台的人。
	// 少了后一道检查，
	// 攻击者可以批量注册一批新号、让它们互相邀请，把活动预算掏空。
	if rule.TriggerType == "invite_reward" {
		if event.InviterID == 0 || event.InviterID == event.UserID {
			return result, errActivityNotApplicable
		}
		established, err := isEstablishedUser(tx, event.InviterID)
		if err != nil {
			return result, err
		}
		if !established {
			return result, errActivityNotApplicable
		}
	}

	// 这里锁住规则行，
	// 避免两个并发事件都读到还剩预算并各花掉最后一份。
	// 表名是显式给的：
	// 否则 GORM 会从结构体名推导出"activity_rules"，而那不是一张真表。
	var locked activityRule
	if err := tx.Table("coupon_activity_rule").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = 'active' AND deleted_at IS NULL", rule.ID).Take(&locked).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, errActivityNotApplicable
		}
		return result, err
	}
	if locked.MaxGrants > 0 {
		granted, err := countRuleGrants(tx, rule)
		if err != nil {
			return result, err
		}
		if granted >= int64(locked.MaxGrants) {
			return result, errActivityNotApplicable
		}
	}
	perUser := locked.PerUserLimit
	if perUser <= 0 {
		perUser = 1
	}
	var used int64
	if err := tx.Table("coupon_grant").
		Where("coupon_id = ? AND user_id = ? AND deleted_at IS NULL", rule.CouponID, event.UserID).
		Count(&used).Error; err != nil {
		return result, err
	}
	if used >= int64(perUser) {
		return result, errActivityNotApplicable
	}

	// 券被删掉后规则还留在表里（规则和券是两张表，删券不删规则）。这时直接
	// 写发放记录会留下一张指向不存在券的券：用户钱包里多出一张永远核销不了
	// 的券，券的库存与单人限领统计也被污染，而且发放记录的 source_event_id
	// 由规则主键算出，下一次同一笔订单再触发就会撞唯一键——一次坏配置能顶
	// 住后面所有结算。规则不适用比发放一张幽灵券更接近真相。
	var couponExists int64
	if err := tx.Table("coupon").
		Where("id = ? AND status = 'active'", locked.CouponID).
		Count(&couponExists).Error; err != nil {
		return result, err
	}
	if couponExists == 0 {
		return result, errActivityNotApplicable
	}

	eventID := activityEventID(rule.ID, event)
	var grantID uint64
	if err := tx.Table("coupon_grant").Create(map[string]any{
		"coupon_id": rule.CouponID, "user_id": event.UserID, "grant_source": grantSourceFor(rule.TriggerType),
		"status": "unused", "expired_at": grantExpiry(tx, rule, event.Now), "source_event_id": eventID,
	}).Error; err != nil {
		return result, err
	}
	if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&grantID).Error; err != nil {
		return result, err
	}
	if err := tx.Table("coupon_grant_request").Create(map[string]any{
		"request_id": eventID, "coupon_id": rule.CouponID, "user_id": event.UserID, "coupon_grant_id": grantID,
	}).Error; err != nil {
		return result, err
	}

	// 邀请人的奖励单独发放，走他自己那份单人预算，
	// 这样一个活跃邀请人既掏不空被邀请人的额度，反过来也一样。
	if rule.TriggerType == "invite_reward" && rule.InviterCouponID.Valid && rule.InviterCouponID.Int64 > 0 {
		granted, err := grantToInviter(tx, rule, event, eventID)
		if err != nil {
			return result, err
		}
		result.InviterGot = granted
	}
	return result, nil
}

func grantToInviter(tx *gorm.DB, rule activityRule, event activityEvent, eventID string) (bool, error) {
	couponID := uint64(rule.InviterCouponID.Int64)
	var used int64
	if err := tx.Table("coupon_grant").
		Where("coupon_id = ? AND user_id = ? AND deleted_at IS NULL", couponID, event.InviterID).
		Count(&used).Error; err != nil {
		return false, err
	}
	perUser := rule.PerUserLimit
	if perUser <= 0 {
		perUser = 1
	}
	if used >= int64(perUser) {
		// 被邀请人照样拿到自己的奖励；
		// 跳过的只是邀请人那一侧，所以邀请人触顶不会让客户损失他应得的券。
		return false, nil
	}
	inviterEvent := activityEvent{UserID: event.InviterID, Now: event.Now}
	inviterRule := rule
	inviterRule.CouponID = couponID
	if err := tx.Table("coupon_grant").Create(map[string]any{
		"coupon_id": couponID, "user_id": event.InviterID, "grant_source": "invite_reward",
		"status": "unused", "expired_at": grantExpiry(tx, inviterRule, event.Now),
		"source_event_id": activityEventID(rule.ID, inviterEvent, "inviter") + ":" + eventID,
	}).Error; err != nil {
		return false, err
	}
	return true, nil
}

// grantSourceFor 把触发类型映射到 coupon_grant 的 source 取值集合。
// 该列是早于活动表存在的枚举，
// 只认 "activity" 和 "invite_reward"，
// 所以各个触发类型都记在它所属的活动名下，而不是记在各自的名字下。
func grantSourceFor(triggerType string) string {
	if triggerType == "invite_reward" {
		return "invite_reward"
	}
	return "activity"
}

// activityEventID 推导幂等键。同一条规则、
// 同一个触发永远算出同一个键，
// 所以重放的支付或结算只会找到已有的发放记录，而不会再次发券。
//
// 规则用主键标识，而不是用 code。
// 发放记录的 source_event_id 就是拿这个键做种子的，
// 而行 id 和 code 一样稳定——
// 它不会在活动上线之后被人偷偷改掉，code 却会。
// 可选的 scope 把邀请人的发放与被邀请人自己的分开，免得一条规则触发一次就撞上自己。
func activityEventID(ruleID uint64, event activityEvent, scope ...string) string {
	var key strings.Builder
	key.WriteString(fmt.Sprint(ruleID))
	for _, s := range scope {
		key.WriteString("\x00" + s)
	}
	sum := sha256.Sum256([]byte(key.String() + "\x00" + fmt.Sprint(event.UserID) + "\x00" + event.EventKey))
	return hex.EncodeToString(sum[:16])
}

// grantExpiry 同时尊重券自身的有效期和活动结束时间，
// 这样券不会活得比发出它的那次活动更久。
func grantExpiry(tx *gorm.DB, rule activityRule, now time.Time) time.Time {
	var coupon struct {
		ValidHours int          `gorm:"column:valid_hours"`
		EndAt      sql.NullTime `gorm:"column:end_at"`
	}
	if err := tx.Table("coupon").Where("id = ? AND deleted_at IS NULL", rule.CouponID).Take(&coupon).Error; err != nil {
		return rule.EndAt
	}
	expires := now.Add(time.Duration(coupon.ValidHours) * time.Hour)
	if coupon.EndAt.Valid && coupon.EndAt.Time.Before(expires) {
		expires = coupon.EndAt.Time
	}
	if rule.EndAt.Before(expires) {
		expires = rule.EndAt
	}
	return expires
}

func countRuleGrants(tx *gorm.DB, rule activityRule) (int64, error) {
	var granted int64
	err := tx.Table("coupon_grant").Where("coupon_id = ? AND grant_source = ? AND deleted_at IS NULL",
		rule.CouponID, grantSourceFor(rule.TriggerType)).Count(&granted).Error
	return granted, err
}

// isEstablishedUser 判断邀请人是否真的用过平台：
// 有一笔已完成的充电，或一笔已结算的充值。
// 仅注册的空号挣不到推荐奖励，这正是挡住一圈空账号刷奖励的那道闸。
func isEstablishedUser(tx *gorm.DB, userID uint64) (bool, error) {
	var orders int64
	if err := tx.Table("charge_order").
		Where("user_id = ? AND status = 'completed' AND deleted_at IS NULL", userID).Count(&orders).Error; err != nil {
		return false, err
	}
	if orders > 0 {
		return true, nil
	}
	var recharges int64
	if err := tx.Table("payment_order").
		Where("user_id = ? AND biz_type = 'wallet_recharge' AND status = 'paid' AND deleted_at IS NULL", userID).
		Count(&recharges).Error; err != nil {
		return false, err
	}
	return recharges > 0, nil
}

// activityEventKeyForRecharge 构造首充奖励的稳定键。
func activityEventKeyForRecharge(requestID string) string { return "recharge:" + requestID }

// activityEventKeyForOrder 构造按订单发放奖励的稳定键。
func activityEventKeyForOrder(orderNo string) string { return "order:" + orderNo }
