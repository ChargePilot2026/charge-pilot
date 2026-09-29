package admin

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Activity rules are the operator-facing side of the coupon campaign engine.
// The engine itself lives in the charge service; this only lets an operator see
// which campaigns are running, open and close them, and check what they cost.
//
// Money is stored in cents and windows are absolute, so a campaign that is
// running is a plain comparison rather than a fuzzy "is it active" flag that can
// disagree with the clock.
var activityRuleCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)

var activityTriggers = map[string]bool{
	"first_recharge": true, "invite_reward": true, "threshold_redeem": true, "holiday": true,
}

type activityRuleRow struct {
	ID              uint64    `json:"id"`
	RuleCode        string    `json:"rule_code"`
	Name            string    `json:"name"`
	TriggerType     string    `json:"trigger_type"`
	CouponID        uint64    `json:"coupon_id"`
	CouponName      string    `json:"coupon_name"`
	InviterCouponID *uint64   `json:"inviter_coupon_id"`
	ThresholdCents  int64     `json:"threshold_cents"`
	MaxGrants       int       `json:"max_grants"`
	GrantedCount    int64     `json:"granted_count"`
	PerUserLimit    int       `json:"per_user_limit"`
	Status          string    `json:"status"`
	StartAt         time.Time `json:"start_at"`
	EndAt           time.Time `json:"end_at"`
	CreatedAt       time.Time `json:"created_at"`
}

func (a ResourceAPI) registerActivityRules(r *gin.Engine) {
	r.GET("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.read"), a.listActivityRules)
	r.POST("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.manage"), a.createActivityRule)
	r.PUT("/api/v1/admin/coupon-activities/:id", a.Auth.Require("coupon.activity.manage"), a.updateActivityRule)
}

// grantedCounts counts what each rule actually handed out, so an operator can
// see a campaign's real cost without leaving the page.
func (a ResourceAPI) grantedCounts(tx *gorm.DB, ruleIDs []uint64) (map[uint64]int64, error) {
	counts := map[uint64]int64{}
	if len(ruleIDs) == 0 {
		return counts, nil
	}
	type row struct {
		CouponID uint64
		Total    int64
	}
	var rows []row
	if err := tx.Table("coupon_grant").
		Select("coupon_id, COUNT(*) AS total").
		Where("coupon_id IN ? AND grant_source IN ('activity','invite_reward') AND deleted_at IS NULL", ruleIDs).
		Group("coupon_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		counts[r.CouponID] = r.Total
	}
	return counts, nil
}

func (a ResourceAPI) listActivityRules(c *gin.Context) {
	page, ok := parsePage(c, "active disabled")
	if !ok {
		return
	}
	out := Page[activityRuleRow]{Items: []activityRuleRow{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.UserDB.WithContext(c.Request.Context()).Table("coupon_activity_rule AS r").
		Joins("LEFT JOIN user_db.coupon AS c ON c.id = r.coupon_id").
		Where("r.deleted_at IS NULL")
	if page.Status != "" {
		query = query.Where("r.status = ?", page.Status)
	}
	if page.Keyword != "" {
		query = query.Where("r.name LIKE ? OR r.rule_code LIKE ?", likePattern(page.Keyword), likePattern(page.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	var rows []activityRuleRow
	if err := query.Session(&gorm.Session{}).
		Select("r.id, r.rule_code, r.name, r.trigger_type, r.coupon_id, c.name AS coupon_name, r.inviter_coupon_id, r.threshold_cents, r.max_grants, r.per_user_limit, r.status, r.start_at, r.end_at, r.created_at").
		Order("r.id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Scan(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.CouponID)
	}
	counts, err := a.grantedCounts(a.Store.UserDB, ids)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	for i := range rows {
		rows[i].GrantedCount = counts[rows[i].CouponID]
	}
	out.Items = rows
	httpapi.OK(c, out)
}

type activityRuleInput struct {
	RuleCode        string  `json:"rule_code"`
	Name            string  `json:"name"`
	TriggerType     string  `json:"trigger_type"`
	CouponID        uint64  `json:"coupon_id"`
	InviterCouponID *uint64 `json:"inviter_coupon_id"`
	ThresholdCents  int64   `json:"threshold_cents"`
	MaxGrants       int     `json:"max_grants"`
	PerUserLimit    int     `json:"per_user_limit"`
	Status          string  `json:"status"`
	StartAt         string  `json:"start_at"`
	EndAt           string  `json:"end_at"`
}

// validate checks a campaign before it is written. The per-trigger rules exist
// because a threshold campaign with no threshold would fire on every order, and
// an invite campaign without a reward for the inviter is just a giveaway.
func (in *activityRuleInput) validate() (time.Time, time.Time, error) {
	if !activityRuleCodePattern.MatchString(in.RuleCode) || in.Name == "" || len([]rune(in.Name)) > 128 {
		return time.Time{}, time.Time{}, errActivityInput
	}
	if !activityTriggers[in.TriggerType] || in.CouponID == 0 {
		return time.Time{}, time.Time{}, errActivityInput
	}
	start, err := time.Parse(time.RFC3339, in.StartAt)
	if err != nil {
		return time.Time{}, time.Time{}, errActivityInput
	}
	end, err := time.Parse(time.RFC3339, in.EndAt)
	if err != nil || !start.Before(end) {
		return time.Time{}, time.Time{}, errActivityInput
	}
	if end.After(time.Now().Add(365 * 24 * time.Hour)) {
		return time.Time{}, time.Time{}, errActivityInput
	}
	if in.ThresholdCents < 0 || in.MaxGrants < 0 || in.PerUserLimit < 0 {
		return time.Time{}, time.Time{}, errActivityInput
	}
	switch in.TriggerType {
	case "threshold_redeem":
		if in.ThresholdCents <= 0 {
			return time.Time{}, time.Time{}, errActivityInput
		}
	case "invite_reward":
		if in.InviterCouponID == nil || *in.InviterCouponID == 0 {
			return time.Time{}, time.Time{}, errActivityInput
		}
	default:
		if in.ThresholdCents != 0 {
			return time.Time{}, time.Time{}, errActivityInput
		}
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.Status != "active" && in.Status != "disabled" {
		return time.Time{}, time.Time{}, errActivityInput
	}
	return start.UTC(), end.UTC(), nil
}

var errActivityInput = errors.New("invalid activity rule")

func (a ResourceAPI) createActivityRule(c *gin.Context) {
	var in activityRuleInput
	if !decodeResource(c, &in) {
		return
	}
	start, end, err := in.validate()
	if err != nil {
		httpapi.BadRequest(c, "活动规则无效：规则码 3–64 位，门槛为非负分，窗口须为 RFC3339 且结束晚于开始，满减须设门槛，邀请有奖须设邀请人券")
		return
	}
	// A rule pointing at a missing or disabled coupon would silently never
	// grant, so it is refused at creation instead.
	if err := a.couponUsable(c, in.CouponID); err != nil {
		httpapi.BadRequest(c, "活动券不存在或已停用")
		return
	}
	if in.InviterCouponID != nil && *in.InviterCouponID != 0 {
		if err := a.couponUsable(c, *in.InviterCouponID); err != nil {
			httpapi.BadRequest(c, "邀请人券不存在或已停用")
			return
		}
	}
	var id uint64
	tx := a.Store.UserDB.WithContext(c.Request.Context())
	err = tx.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("coupon_activity_rule").Create(map[string]any{
			"rule_code": in.RuleCode, "name": in.Name, "trigger_type": in.TriggerType,
			"coupon_id": in.CouponID, "inviter_coupon_id": in.InviterCouponID,
			"threshold_cents": in.ThresholdCents, "max_grants": in.MaxGrants,
			"per_user_limit": in.PerUserLimit, "status": in.Status, "start_at": start, "end_at": end,
		}).Error; err != nil {
			return err
		}
		return tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	// The rule lives in user_db and audit_log lives in admin_db. Joining them in
	// one transaction would be a cross-schema write, which this project does not
	// do, so the audit is written through AdminDB once the change has committed.
	a.auditToAdmin(c, "create", "coupon_activity", id, nil, in, "")
	httpapi.OK(c, gin.H{"id": id})
}

func (a ResourceAPI) updateActivityRule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in activityRuleInput
	if !decodeResource(c, &in) {
		return
	}
	start, end, err := in.validate()
	if err != nil {
		httpapi.BadRequest(c, "活动规则无效")
		return
	}
	if err := a.couponUsable(c, in.CouponID); err != nil {
		httpapi.BadRequest(c, "活动券不存在或已停用")
		return
	}
	// A typed snapshot, not `any`: GORM reflects on the destination and panics
	// on a nil interface.
	var before activityRuleRow
	if err := a.Store.UserDB.WithContext(c.Request.Context()).
		Table("coupon_activity_rule").Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, http.StatusNotFound, 1004, "活动规则不存在", nil)
			return
		}
		resourceFailure(c, err)
		return
	}
	err = a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		changed := tx.Table("coupon_activity_rule").Where("id = ? AND deleted_at IS NULL", id).
			Updates(map[string]any{
				"name": in.Name, "coupon_id": in.CouponID, "inviter_coupon_id": in.InviterCouponID,
				"threshold_cents": in.ThresholdCents, "max_grants": in.MaxGrants,
				"per_user_limit": in.PerUserLimit, "status": in.Status, "start_at": start, "end_at": end,
			})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.auditToAdmin(c, "update", "coupon_activity", id, before, in, "")
	httpapi.OK(c, gin.H{"id": id})
}

// couponUsable refuses a campaign that points at a coupon which cannot be
// granted, because a rule that silently never fires is indistinguishable from a
// working one until the campaign is over.
func (a ResourceAPI) couponUsable(c *gin.Context, couponID uint64) error {
	var count int64
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("coupon").
		Where("id = ? AND status = 'active' AND deleted_at IS NULL", couponID).Count(&count).Error; err != nil {
		return err
	}
	// A count of zero is not an error, so it has to be checked explicitly: a
	// rule pointing at a coupon that does not exist would never grant anything
	// and would look exactly like a working campaign until it expired.
	if count == 0 {
		return errActivityInput
	}
	return nil
}
