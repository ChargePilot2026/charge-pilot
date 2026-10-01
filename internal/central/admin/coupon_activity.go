package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 本文件提供优惠券活动规则的运营接口；发放逻辑由 charge 包实现。
// 金额单位为分，活动时间使用绝对起止时间，是否生效由状态及时间窗口共同判断。
var activityTriggers = map[string]bool{
	"first_recharge": true, "invite_reward": true, "threshold_redeem": true, "holiday": true,
}

// activityRuleRow 是活动列表投影，包含规则、券名称和累计发放数量。
type activityRuleRow struct {
	ID              uint64    `json:"id"`                // 活动规则主键
	Name            string    `json:"name"`              // 活动名称，运营可读，不参与业务判定
	TriggerType     string    `json:"trigger_type"`      // 触发类型：first_recharge 首充、invite_reward 邀请有奖、threshold_redeem 满减、holiday 节日
	CouponID        uint64    `json:"coupon_id"`         // 活动发放的券 ID
	CouponName      string    `json:"coupon_name"`       // 关联券的名称，从 central_db.coupon 联查出来只用于展示
	InviterCouponID *uint64   `json:"inviter_coupon_id"` // 邀请人奖励券 ID，指针为 nil 表示该活动不是邀请有奖类型
	ThresholdCents  int64     `json:"threshold_cents"`   // 触发门槛，单位分；满减类必填，其余类型必须为 0
	MaxGrants       int       `json:"max_grants"`        // 活动总发放上限，0 表示不限；用于控制活动成本
	GrantedCount    int64     `json:"granted_count"`     // 该券由活动/邀请奖励实际发出的份数，聚合 coupon_grant 得出，不落库
	PerUserLimit    int       `json:"per_user_limit"`    // 单用户可领份数，0 表示不限
	Status          string    `json:"status"`            // 活动状态：active 进行中 / disabled 已停用
	StartAt         time.Time `json:"start_at"`          // 活动开始时间，UTC 绝对时间，是否在进行中由引擎按时间比较得出
	EndAt           time.Time `json:"end_at"`            // 活动结束时间，UTC 绝对时间，须晚于开始时间
	CreatedAt       time.Time `json:"created_at"`        // 创建时间
}

// registerActivityRules 挂载券活动的后台路由：列表（只读）、新建、修改，均为管理后台侧，
// 活动引擎本身在 charge 服务里，这里只负责运营的查看与开关。
func (a ResourceAPI) registerActivityRules(r *gin.Engine) {
	r.GET("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.read"), a.listActivityRules)
	r.POST("/api/v1/admin/coupon-activities", a.Auth.Require("coupon.activity.manage"), a.createActivityRule)
	r.PUT("/api/v1/admin/coupon-activities/:id", a.Auth.Require("coupon.activity.manage"), a.updateActivityRule)
}

// grantedCounts 按券 ID 聚合 activity 和 invite_reward 来源的发放数量。
// 不同活动引用同一张券时，共享该券的累计计数。
func (a ResourceAPI) grantedCounts(tx *gorm.DB, ruleIDs []uint64) (map[uint64]int64, error) {
	counts := map[uint64]int64{}
	if len(ruleIDs) == 0 {
		return counts, nil
	}
	type row struct {
		CouponID uint64 // 被统计的券 ID，作为 map 的键
		Total    int64  // 该券的活动类发放总份数
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

// listActivityRules 分页返回券活动列表，支持按状态（active/disabled）和活动名关键词过滤，
// 并把每条活动券的实际发放份数补进 GrantedCount，让运营直接看到活动成本。
func (a ResourceAPI) listActivityRules(c *gin.Context) {
	page, ok := parsePage(c, "active disabled")
	if !ok {
		return
	}
	out := Page[activityRuleRow]{Items: []activityRuleRow{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.UserDB.WithContext(c.Request.Context()).Table("coupon_activity_rule AS r").
		Joins("LEFT JOIN coupon AS c ON c.id = r.coupon_id").
		Where("r.deleted_at IS NULL")
	if page.Status != "" {
		query = query.Where("r.status = ?", page.Status)
	}
	if page.Keyword != "" {
		query = query.Where("r.name LIKE ?", likePattern(page.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	var rows []activityRuleRow
	if err := query.Session(&gorm.Session{}).
		Select("r.id, r.name, r.trigger_type, r.coupon_id, c.name AS coupon_name, r.inviter_coupon_id, r.threshold_cents, r.max_grants, r.per_user_limit, r.status, r.start_at, r.end_at, r.created_at").
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

// activityRuleInput 是创建和更新活动的请求体。
// StartAt 与 EndAt 由 validate 按 RFC3339 统一解析并返回参数错误。
type activityRuleInput struct {
	Name            string  `json:"name"`              // 活动名称，必填，最多 128 字
	TriggerType     string  `json:"trigger_type"`      // 触发类型，取值见 activityTriggers
	CouponID        uint64  `json:"coupon_id"`         // 活动发放的券 ID，必填且必须存在且为 active
	InviterCouponID *uint64 `json:"inviter_coupon_id"` // 邀请人奖励券 ID；邀请有奖类型必填且不得为 0，其余类型可为 nil
	ThresholdCents  int64   `json:"threshold_cents"`   // 触发门槛（分）；满减类型须 > 0，其余类型须为 0
	MaxGrants       int     `json:"max_grants"`        // 活动总发放上限，0 表示不限
	PerUserLimit    int     `json:"per_user_limit"`    // 单用户可领份数，0 表示不限
	Status          string  `json:"status"`            // active 或 disabled；留空时默认置为 active
	StartAt         string  `json:"start_at"`          // 活动开始时间，RFC3339 字符串
	EndAt           string  `json:"end_at"`            // 活动结束时间，RFC3339 字符串，须晚于开始时间且不超过一年后
}

// validate 检查活动参数及触发类型约束：满减必须有门槛，邀请活动必须配置邀请人奖励券。
// 首充和节日活动不接受门槛；缺省状态设为 active，起止时间转换为 UTC。
func (in *activityRuleInput) validate() (time.Time, time.Time, error) {
	if in.Name == "" || len([]rune(in.Name)) > 128 {
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

// errActivityInput 是券活动各类校验失败的统一哨兵错误，只在事务内传播，由外层翻译成给运营看的中文提示。
var errActivityInput = errors.New("invalid activity rule")

// createActivityRule 新建一个券活动。校验通过且所引用的券都可用后写入 coupon_activity_rule。
func (a ResourceAPI) createActivityRule(c *gin.Context) {
	var in activityRuleInput
	if !decodeResource(c, &in) {
		return
	}
	start, end, err := in.validate()
	if err != nil {
		httpapi.BadRequest(c, "活动规则无效：名称不能为空，门槛为非负分，窗口须为 RFC3339 且结束晚于开始，满减须设门槛，邀请有奖须设邀请人券")
		return
	}
	// 创建前校验奖励券存在且可用，避免保存无法发券的规则。
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
			"name": in.Name, "trigger_type": in.TriggerType,
			"coupon_id": in.CouponID, "inviter_coupon_id": in.InviterCouponID,
			"threshold_cents": in.ThresholdCents, "max_grants": in.MaxGrants,
			"per_user_limit": in.PerUserLimit, "status": in.Status, "start_at": start, "end_at": end,
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "create", "coupon_activity", id, nil, in, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// updateActivityRule 更新活动配置但保持 trigger_type 不变，确保历史发放的触发语义一致。
// 更新前读取旧值用于审计；规则不存在时返回 404。
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
	// 使用具体类型接收审计快照，避免 GORM 反射 nil 接口时 panic。
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
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "update", "coupon_activity", id, before, in, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// couponUsable 校验券存在、状态为 active 且未软删除；不满足时返回 errActivityInput。
func (a ResourceAPI) couponUsable(c *gin.Context, couponID uint64) error {
	var count int64
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("coupon").
		Where("id = ? AND status = 'active' AND deleted_at IS NULL", couponID).Count(&count).Error; err != nil {
		return err
	}
	// Count 不将零行视为错误，因此必须显式拒绝不存在或不可用的券。
	if count == 0 {
		return errActivityInput
	}
	return nil
}
