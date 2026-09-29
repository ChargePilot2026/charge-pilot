package charge

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Activity rules hand out coupons when a customer meets a condition. The coupon
// itself lives in `coupon`; a rule only decides when one is granted and how far
// the campaign may run.
//
// Every grant is written with a deterministic source_event_id derived from the
// rule, the customer and the triggering fact. That is what makes a replayed
// event a no-op instead of a second coupon: the same trigger always produces
// the same key, and the unique constraint behind coupon_grant_request turns a
// repeat into a duplicate error we can swallow.
//
// A rule that cannot be satisfied is not an error. Campaigns expire, run out of
// budget, or lose a race, and none of that should fail the payment or order that
// triggered them, so evaluation reports what it granted and leaves the rest.
var errActivityNotApplicable = errors.New("no activity rule applies")

// activityRule is the subset of a rule the engine needs. Reading it as a struct
// rather than a map keeps a schema change from silently changing behaviour.
type activityRule struct {
	ID              uint64        `gorm:"column:id"`
	RuleCode        string        `gorm:"column:rule_code"`
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

// activityEvent identifies what happened. EventKey must be stable across
// retries: the same real-world fact has to produce the same key, because that
// key is what makes the grant idempotent.
type activityEvent struct {
	TriggerType string
	UserID      uint64
	// EventKey distinguishes repeated occurrences of the same trigger, e.g. two
	// different recharge request ids for the same customer.
	EventKey string
	// AmountCents is only read by threshold rules.
	AmountCents int64
	// InviterID is only used by invite_reward.
	InviterID uint64
	// Now is injected so tests do not depend on the wall clock.
	Now time.Time
}

type activityResult struct {
	RuleCode   string `json:"rule_code"`
	CouponID   uint64 `json:"coupon_id"`
	InviterGot bool   `json:"inviter_rewarded"`
	Already    bool   `json:"already_granted"`
}

// ApplyActivityRules grants whatever the customer's event qualifies for. It is
// called from inside the transaction that settled the triggering fact, so a
// rule can never pay out for a payment that then rolls back.
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
	result := activityResult{RuleCode: rule.RuleCode, CouponID: rule.CouponID}

	// A customer cannot invite themselves, and the inviter must be someone who
	// already used the platform. Without the second check an attacker can
	// register a batch of fresh accounts, have them invite each other, and drain
	// the campaign.
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

	// The rule row is locked so two concurrent events cannot both read a
	// remaining budget and both spend the last one.
	// The table name is given explicitly: GORM would otherwise derive
	// "activity_rules" from the struct name, which is not a real table.
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

	eventID := activityEventID(rule.RuleCode, event)
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

	// The inviter is paid separately, under their own per-user budget, so one
	// busy inviter cannot drain the invitee's allocation or vice versa.
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
		// The invitee still keeps their reward; only the inviter's side is
		// skipped, so a capped inviter never costs the customer their coupon.
		return false, nil
	}
	inviterEvent := activityEvent{UserID: event.InviterID, Now: event.Now}
	inviterRule := rule
	inviterRule.CouponID = couponID
	if err := tx.Table("coupon_grant").Create(map[string]any{
		"coupon_id": couponID, "user_id": event.InviterID, "grant_source": "invite_reward",
		"status": "unused", "expired_at": grantExpiry(tx, inviterRule, event.Now),
		"source_event_id": activityEventID(rule.RuleCode+":inviter", inviterEvent) + ":" + eventID,
	}).Error; err != nil {
		return false, err
	}
	return true, nil
}

// grantSourceFor maps a trigger onto the coupon_grant source vocabulary. The
// column is an enum that predates the activity table and only knows "activity"
// and "invite_reward", so the individual triggers are recorded under the
// campaign they belong to rather than under their own name.
func grantSourceFor(triggerType string) string {
	if triggerType == "invite_reward" {
		return "invite_reward"
	}
	return "activity"
}

// activityEventID derives the idempotency key. The same rule and the same
// trigger always produce the same key, so a replayed payment or settlement finds
// the existing grant instead of paying out again.
func activityEventID(ruleCode string, event activityEvent) string {
	sum := sha256.Sum256([]byte(ruleCode + "\x00" + fmt.Sprint(event.UserID) + "\x00" + event.EventKey))
	return hex.EncodeToString(sum[:16])
}

// grantExpiry respects both the coupon's own validity window and the campaign's
// end, so a coupon cannot outlive the activity that handed it out.
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

// isEstablishedUser reports whether the inviter has actually used the platform:
// a completed charge or a settled recharge. Fresh sign-ups alone cannot earn a
// referral reward, which is what stops a ring of empty accounts from farming.
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

// activityEventKeyForRecharge builds the stable key for a first-recharge reward.
func activityEventKeyForRecharge(requestID string) string { return "recharge:" + requestID }

// activityEventKeyForOrder builds the stable key for an order-based reward.
func activityEventKeyForOrder(orderNo string) string { return "order:" + orderNo }
