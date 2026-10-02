package admin

import (
	"context"
	"net/url"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/phone"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 本文件提供 central_db.user 的充电用户查询，不操作后台管理员账号。

// ChargeUserRow 是后台充电用户列表的一行。
type ChargeUserRow struct {
	ID        uint64     `json:"id,string" gorm:"column:id"`                 // 充电用户主键，订单/钱包/券/报障都以它为外键
	OpenID    string     `json:"openid" gorm:"column:openid"`                // 微信 openid，唯一登录身份
	UnionID   *string    `json:"union_id" gorm:"column:unionid"`             // 微信 unionid；指针，仅同一开放平台账号下有值
	Nickname  *string    `json:"nickname" gorm:"column:nickname"`            // 微信昵称；指针，未授权昵称时为 null，前端需回退到"用户 #id"
	AvatarURL *string    `json:"avatar_url" gorm:"column:avatar_url"`        // 头像地址；指针，同上
	Gender    string     `json:"gender" gorm:"column:gender"`                // 性别：unknown/male/female，未授权恒为 unknown
	Status    string     `json:"status" gorm:"column:status"`                // 账号状态：active 正常 / frozen 冻结
	FirstSeen time.Time  `json:"first_seen_at" gorm:"column:first_seen_at"`  // 首次出现时间，即注册时间
	LastLogin *time.Time `json:"last_login_at" gorm:"column:last_login_at"`  // 最后登录时间；指针，从未登录过为 null
	InviterID *uint64    `json:"inviter_id,string" gorm:"column:inviter_id"` // 邀请人用户 ID；指针，自然注册无邀请人
	CreatedAt time.Time  `json:"created_at" gorm:"column:created_at"`        // 记录创建时间

	// 以下几列不在主查询里联表，由 decorateChargeUsers 按当页 ID 批量回填。
	Phone        string  `json:"phone" gorm:"-"`         // 完整明文手机号；未绑号为空串
	PhoneBound   bool    `json:"phone_bound" gorm:"-"`   // 是否已绑定手机号
	BalanceCents int64   `json:"balance_cents" gorm:"-"` // 钱包可用余额（分）
	FrozenCents  int64   `json:"frozen_cents" gorm:"-"`  // 钱包冻结金额（分）
	OrderCount   int64   `json:"order_count" gorm:"-"`   // 历史充电订单总数
	TotalCents   int64   `json:"total_cents" gorm:"-"`   // 历史累计消费（分）
	LastOrderAt  *string `json:"last_order_at" gorm:"-"` // 最近一次下单时间；指针，从未下过单为 null
}

// TableName 将列表与内嵌该结构的详情模型映射到 user 表，避免 GORM 推导错误表名。
func (ChargeUserRow) TableName() string { return "user" }

// ChargeUserDetail 是单个充电用户的档案，列表行的超集。
type ChargeUserDetail struct {
	ChargeUserRow
	WalletStatus  string                 `json:"wallet_status"`  // 钱包状态：active/frozen
	RecentOrders  []ChargeUserOrderBrief `json:"recent_orders"`  // 最近订单，按下单时间倒序
	CouponGranted int64                  `json:"coupon_granted"` // 累计发放的优惠券张数
	CouponUnused  int64                  `json:"coupon_unused"`  // 当前未使用的优惠券张数
	FaultReports  int64                  `json:"fault_reports"`  // 该用户提交的报障单数
}

// ChargeUserOrderBrief 是用户档案中的最近订单摘要，包含业务、支付状态和关键时间。
type ChargeUserOrderBrief struct {
	OrderID        uint64  `json:"order_id" gorm:"column:id"`                     // 订单主键
	OrderNo        string  `json:"order_no" gorm:"column:order_no"`               // 订单号，用户报障时会向客服念这个
	DeviceID       string  `json:"device_id" gorm:"column:device_id"`             // 设备编号
	StationID      *uint64 `json:"station_id" gorm:"column:station_id"`           // 站点 ID；指针，未关联为 null
	Status         string  `json:"status" gorm:"column:status"`                   // 原内部生命周期，供异常提示与兼容使用。
	BusinessStatus string  `json:"business_status" gorm:"column:business_status"` // 独立业务状态：待启动、充电中、已完成。
	PaymentStatus  string  `json:"payment_status" gorm:"column:payment_status"`   // 独立支付状态，读取实际到账/退款后持久化的值。
	TotalCents     *int64  `json:"total_cents" gorm:"column:total_cents"`         // 应收合计（分）；指针，未计费为 null
	CreatedAt      string  `json:"created_at" gorm:"column:created_at"`           // 下单时间
	StartedAt      *string `json:"started_at" gorm:"column:started_at"`           // 开始充电时间；指针，未开始为 null
}

// chargeUserDetailOrderLimit 限制档案中的最近订单数；完整历史通过订单列表按 user_id 查询。
const chargeUserDetailOrderLimit = 20

// chargeUserStat 是一次性聚合出来的派生数据，避免列表页按行查库。
type chargeUserStat struct {
	Phone        string     // 该用户的明文手机号
	BalanceCents int64      // 钱包可用余额（分）
	FrozenCents  int64      // 钱包冻结金额（分）
	OrderCount   int64      // 历史订单数
	TotalCents   int64      // 历史累计消费（分）
	LastOrderAt  *time.Time // 最近下单时间
}

// registerChargeUsers 注册用户列表、档案和充值记录，要求 user.read 权限。
// 账号冻结和解冻由风控流程处理。
func (a ResourceAPI) registerChargeUsers(r *gin.Engine) {
	r.GET("/api/v1/admin/users", a.Auth.Require("user.read"), a.chargeUsers)
	r.GET("/api/v1/admin/users/:id", a.Auth.Require("user.read"), a.chargeUser)
	r.GET("/api/v1/admin/users/:id/recharges", a.Auth.Require("user.read"), a.chargeUserRecharges)
}

// chargeUsers 分页列出充电用户。
//
// 关键词匹配昵称、openid 和手机号；完整手机号精确查询，号码片段支持模糊查询。
func (a ResourceAPI) chargeUsers(c *gin.Context) {
	q, ok := parsePage(c, "active frozen")
	if !ok {
		return
	}
	rows, total, err := a.Store.ChargeUsers(c.Request.Context(), q)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if err := a.decorateChargeUsers(c.Request.Context(), rows); err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, Page[ChargeUserRow]{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize,
		Permissions: c.MustGet("admin_profile").(Profile).Permissions})
}

// chargeUser 返回单个充电用户的完整档案：钱包、累计消费、最近订单、券与报障计数。
func (a ResourceAPI) chargeUser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	detail, err := a.Store.ChargeUserDetail(c.Request.Context(), id, chargeUserDetailOrderLimit)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	row := []ChargeUserRow{detail.ChargeUserRow}
	if err := a.decorateChargeUsers(c.Request.Context(), row); err != nil {
		resourceFailure(c, err)
		return
	}
	detail.ChargeUserRow = row[0]
	httpapi.OK(c, detail)
}

// chargeUserRecharges 分页读取路径用户的充值支付单，查询参数不能改变用户或业务范围。
func (a ResourceAPI) chargeUserRecharges(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	params, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		httpapi.BadRequest(c, "查询参数格式无效")
		return
	}
	for name, values := range params {
		if (name != "page" && name != "page_size") || len(values) != 1 || values[0] == "" {
			httpapi.BadRequest(c, "仅支持单值 page 和 page_size 分页参数")
			return
		}
	}
	page, ok := parsePage(c, "")
	if !ok {
		return
	}
	out, err := a.Store.ChargeUserRecharges(c.Request.Context(), id, page)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// ChargeUserRecharges 只返回未删除用户的余额充值，不把未支付订单视为到账。
func (s ResourceStore) ChargeUserRecharges(ctx context.Context, id uint64, page PageQuery) (Page[PaymentOrderView], error) {
	var user struct{ ID uint64 }
	if err := s.UserDB.WithContext(ctx).Table("user").Select("id").Where("id=? AND deleted_at IS NULL", id).Take(&user).Error; err != nil {
		return Page[PaymentOrderView]{}, err
	}
	return s.PaymentOrders(ctx, PaymentOrderQuery{PageQuery: page, UserID: id, BizType: "wallet_recharge"})
}

// decorateChargeUsers 批量回填手机号、钱包余额与订单统计。
func (a ResourceAPI) decorateChargeUsers(ctx context.Context, rows []ChargeUserRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	stats, err := a.Store.ChargeUserStats(ctx, ids)
	if err != nil {
		return err
	}
	for i := range rows {
		stat, ok := stats[rows[i].ID]
		if !ok {
			continue
		}
		rows[i].BalanceCents = stat.BalanceCents
		rows[i].FrozenCents = stat.FrozenCents
		rows[i].OrderCount = stat.OrderCount
		rows[i].TotalCents = stat.TotalCents
		if stat.LastOrderAt != nil {
			formatted := stat.LastOrderAt.UTC().Format(time.RFC3339)
			rows[i].LastOrderAt = &formatted
		}
		rows[i].Phone = stat.Phone
		rows[i].PhoneBound = stat.Phone != ""
	}
	return nil
}

// ChargeUsers 按关键词与状态分页查出充电用户主记录，不含任何派生列。
func (s ResourceStore) ChargeUsers(ctx context.Context, q PageQuery) ([]ChargeUserRow, int64, error) {
	out := []ChargeUserRow{}
	query := s.UserDB.WithContext(ctx).Model(&ChargeUserRow{}).Where("deleted_at IS NULL")
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		if phone.Valid(q.Keyword) {
			query = query.Where("phone = ?", phone.Normalize(q.Keyword))
		} else {
			pattern := likePattern(q.Keyword)
			query = query.Where("nickname LIKE ? ESCAPE '!' OR openid LIKE ? ESCAPE '!' OR unionid LIKE ? ESCAPE '!' OR phone LIKE ? ESCAPE '!'", pattern, pattern, pattern, pattern)
		}
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	// 按最近登录时间和 ID 倒序排列；last_login_at 为 NULL 的用户排在末尾。
	err := query.Order("last_login_at DESC, id DESC").
		Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out).Error
	return out, total, err
}

// ChargeUserStats 一次性取回一批用户的手机号、钱包余额与订单聚合，避免按行查库。
func (s ResourceStore) ChargeUserStats(ctx context.Context, ids []uint64) (map[uint64]chargeUserStat, error) {
	stats := make(map[uint64]chargeUserStat, len(ids))

	var users []struct {
		ID    uint64  `gorm:"column:id"`
		Phone *string `gorm:"column:phone"`
	}
	if err := s.UserDB.WithContext(ctx).Raw(
		"SELECT id, phone FROM user WHERE id IN ?", ids).Scan(&users).Error; err != nil {
		return nil, err
	}
	for _, u := range users {
		stat := stats[u.ID]
		if u.Phone != nil {
			stat.Phone = *u.Phone
		}
		stats[u.ID] = stat
	}

	// 批量查询未删除的钱包记录；不假定历史数据严格满足每用户单一钱包。
	var wallets []struct {
		UserID       uint64 `gorm:"column:user_id"`
		BalanceCents int64  `gorm:"column:balance_cents"`
		FrozenCents  int64  `gorm:"column:frozen_cents"`
	}
	if err := s.UserDB.WithContext(ctx).Raw(
		"SELECT user_id, balance_cents, frozen_cents FROM wallet_account WHERE deleted_at IS NULL AND user_id IN ?", ids).
		Scan(&wallets).Error; err != nil {
		return nil, err
	}
	for _, w := range wallets {
		stat := stats[w.UserID]
		stat.BalanceCents = w.BalanceCents
		stat.FrozenCents = w.FrozenCents
		stats[w.UserID] = stat
	}

	// 累计消费仅统计已计费金额；NULL 通过 COALESCE 按 0 处理。
	var orders []struct {
		UserID      uint64     `gorm:"column:user_id"`
		OrderCount  int64      `gorm:"column:order_count"`
		TotalCents  int64      `gorm:"column:total_cents"`
		LastOrderAt *time.Time `gorm:"column:last_order_at"`
	}
	if err := s.UserDB.WithContext(ctx).Raw(`
		SELECT user_id, COUNT(*) AS order_count,
		       COALESCE(SUM(total_cents), 0) AS total_cents,
		       MAX(created_at) AS last_order_at
		FROM charge_order
		WHERE deleted_at IS NULL AND user_id IN ?
		GROUP BY user_id`, ids).Scan(&orders).Error; err != nil {
		return nil, err
	}
	for _, o := range orders {
		stat := stats[o.UserID]
		stat.OrderCount = o.OrderCount
		stat.TotalCents = o.TotalCents
		stat.LastOrderAt = o.LastOrderAt
		stats[o.UserID] = stat
	}
	return stats, nil
}

// ChargeUserDetail 组装单个充电用户的档案：主记录、钱包状态、最近订单、券与报障计数。
func (s ResourceStore) ChargeUserDetail(ctx context.Context, id uint64, orderLimit int) (ChargeUserDetail, error) {
	detail := ChargeUserDetail{}
	if err := s.UserDB.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", id).Take(&detail.ChargeUserRow).Error; err != nil {
		return detail, err
	}
	if err := s.UserDB.WithContext(ctx).Raw(
		"SELECT status FROM wallet_account WHERE user_id = ? AND deleted_at IS NULL LIMIT 1", id).
		Scan(&detail.WalletStatus).Error; err != nil {
		return detail, err
	}

	detail.RecentOrders = []ChargeUserOrderBrief{}
	// 通过 charge_payment_intent 补充站点 ID，其订单唯一键保证连接不增加行数。
	// 缺少支付意图的历史订单返回 NULL 站点。
	if err := s.UserDB.WithContext(ctx).Raw(`
		SELECT c.id, c.order_no, c.device_id, i.station_id, c.status, c.business_status, c.payment_status, c.total_cents,
		       c.created_at, c.started_at
		FROM charge_order c
		LEFT JOIN charge_payment_intent i ON i.charge_order_id = c.id
		WHERE c.user_id = ? AND c.deleted_at IS NULL
		ORDER BY c.id DESC
		LIMIT ?`, id, orderLimit).Scan(&detail.RecentOrders).Error; err != nil {
		return detail, err
	}

	// 仅聚合优惠券和报障数量，不加载明细。
	// 使用独立计数结构扫描，避免 GORM 将详情模型的 RecentOrders 切片解析为数据库关联。
	var counts struct {
		CouponGranted int64 `gorm:"column:coupon_granted"`
		CouponUnused  int64 `gorm:"column:coupon_unused"`
		FaultReports  int64 `gorm:"column:fault_reports"`
	}
	if err := s.UserDB.WithContext(ctx).Raw(`
		SELECT
		  (SELECT COUNT(*) FROM coupon_grant WHERE user_id = ? AND deleted_at IS NULL) AS coupon_granted,
		  (SELECT COUNT(*) FROM coupon_grant WHERE user_id = ? AND deleted_at IS NULL AND status = 'unused') AS coupon_unused,
		  (SELECT COUNT(*) FROM device_fault_report WHERE user_id = ? AND deleted_at IS NULL) AS fault_reports`,
		id, id, id).Scan(&counts).Error; err != nil {
		return detail, err
	}
	detail.CouponGranted = counts.CouponGranted
	detail.CouponUnused = counts.CouponUnused
	detail.FaultReports = counts.FaultReports
	return detail, nil
}
