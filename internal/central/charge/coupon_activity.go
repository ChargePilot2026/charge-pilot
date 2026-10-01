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

// errActivityNotApplicable 表示规则过期、预算不足或并发领取未成功；调用方将其视为正常跳过。
var errActivityNotApplicable = errors.New("no activity rule applies")

// activityRule 是活动求值所需的类型化数据库字段投影。
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

// activityEvent 描述触发事件；EventKey 必须在重试间保持稳定，以生成相同的发放幂等键。
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

// ApplyActivityRules 在触发事件的业务事务中发放符合条件的优惠券。
// 规则限定时间、预算和领取条件；奖励券来自 coupon 表。
// 规则、用户及事件共同生成 source_event_id，由 coupon_grant_request 唯一约束防止重复发放。
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

	// 禁止自邀；邀请人必须已有已完成充电或已结算充值，防止空账号互邀消耗活动预算。
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

	// 锁定活动规则行后检查预算，串行化并发发放。
	// 显式指定 coupon_activity_rule 表名，避免 GORM 按结构体名推导。
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

	// 发放前校验券仍存在且可用；删除券不会自动删除规则。
	// 无效券直接跳过，避免生成无法核销的记录、消耗库存或占用幂等键。
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

	// 邀请人与被邀请人的奖励分别占用各自的单人额度。
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
		// 邀请人达到领取上限时仅跳过邀请人奖励，不影响被邀请人的奖励。
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

// grantSourceFor 将活动触发类型映射到 coupon_grant 支持的 activity 或 invite_reward 来源。
func grantSourceFor(triggerType string) string {
	if triggerType == "invite_reward" {
		return "invite_reward"
	}
	return "activity"
}

// activityEventID 根据规则主键、用户、事件键和可选 scope 生成确定性发放键。
// 重复事件使用相同键；scope 区分邀请人与被邀请人的奖励，避免互相冲突。
func activityEventID(ruleID uint64, event activityEvent, scope ...string) string {
	var key strings.Builder
	key.WriteString(fmt.Sprint(ruleID))
	for _, s := range scope {
		key.WriteString("\x00" + s)
	}
	sum := sha256.Sum256([]byte(key.String() + "\x00" + fmt.Sprint(event.UserID) + "\x00" + event.EventKey))
	return hex.EncodeToString(sum[:16])
}

// grantExpiry 返回券有效期与活动结束时间中的较早值。
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

// isEstablishedUser 判断邀请人是否存在已完成充电或已结算充值；仅注册的用户不满足条件。
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
