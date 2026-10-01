package admin

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/phonecrypto"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 充电用户（charge user）是 user_db.user 里的 C 端用户，和 admin_db 的后台管理员账号是两回事。
//
// 此前后台能看到订单、反馈、报障，但每处都只带一个裸的 user_id：客服接到投诉时
// 无从知道这个人是谁、怎么联系他，运营想核对一个用户的消费也只能挨个翻订单。
// 这个资源补的就是这段断层——它是后台第一个以"人"而不是以"单"为入口的视图。

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
	// 手机号尤其只能这么做：user.phone_enc 是 AES-GCM 密文，phone_hash 是不可逆的
	// SHA-256，库里没有任何可排序、可模糊匹配的明文列，LIKE 无从下手。
	Phone        string  `json:"phone" gorm:"-"`         // 完整手机号，由 phone_enc 解密而来；未绑号为空串
	PhoneBound   bool    `json:"phone_bound" gorm:"-"`   // 是否已绑定手机号
	BalanceCents int64   `json:"balance_cents" gorm:"-"` // 钱包可用余额（分）
	FrozenCents  int64   `json:"frozen_cents" gorm:"-"`  // 钱包冻结金额（分）
	OrderCount   int64   `json:"order_count" gorm:"-"`   // 历史充电订单总数
	TotalCents   int64   `json:"total_cents" gorm:"-"`   // 历史累计消费（分）
	LastOrderAt  *string `json:"last_order_at" gorm:"-"` // 最近一次下单时间；指针，从未下过单为 null
}

// TableName 把这个结构体指回 user_db.user。
//
// 不能省。GORM 默认按结构体名推导表名，ChargeUserRow 会被推成 charge_user_rows，
// 而真实表名是 user——少了这一行，列表和详情都会以"表不存在"失败，而报错信息
// 只说读不到数据，不提表名。ChargeUserDetail 内嵌本结构体，共用同一个方法。
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

// ChargeUserOrderBrief 是档案里附带的订单摘要，字段取客服场景真正要看的那些。
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

// chargeUserDetailOrderLimit 限制档案里附带的最近订单条数。客服要的是"最近发生了什么"，
// 不是完整历史——完整历史由订单页按 user_id 过滤承担。
const chargeUserDetailOrderLimit = 20

// chargeUserStat 是一次性聚合出来的派生数据，避免列表页按行查库。
type chargeUserStat struct {
	PhoneEnc     []byte     // 该用户的手机号密文
	BalanceCents int64      // 钱包可用余额（分）
	FrozenCents  int64      // 钱包冻结金额（分）
	OrderCount   int64      // 历史订单数
	TotalCents   int64      // 历史累计消费（分）
	LastOrderAt  *time.Time // 最近下单时间
}

// registerChargeUsers 挂载充电用户的只读接口。
//
// 整个资源只有读权限，没有建号、改状态、解冻这些写操作。冻结是风控的动作，
// 不在这里重复开一个入口——两个入口能各自改同一列，迟早出现"一边解冻一边还在拦截"。
func (a ResourceAPI) registerChargeUsers(r *gin.Engine) {
	r.GET("/api/v1/admin/charge-users", a.Auth.Require("charge_user.read"), a.chargeUsers)
	r.GET("/api/v1/admin/charge-users/:id", a.Auth.Require("charge_user.read"), a.chargeUser)
}

// chargeUsers 分页列出充电用户。
//
// 关键词匹配昵称、openid 和手机号。手机号走 phone_hash 等值匹配：库里存的是密文加
// 不可逆哈希，没有可 LIKE 的明文，所以只支持输入完整 11 位号码精确查询，输入"后四位"
// 这类片段查不出来。这是加密存储的必然代价，不是搜索失灵。
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

// decorateChargeUsers 批量回填列表行的派生列：解密手机号、联钱包、统计订单。
//
// 解密只能逐条做，所以这一步的成本与当页条数成正比。page_size 上限 100 有相当一部分
// 就是这个原因——再放大，一次列表请求会明显变慢。
//
// 没有配置密钥时不解密，但整页照常返回：看不到号码总比看不到用户强。
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
		if len(stat.PhoneEnc) > 0 {
			rows[i].PhoneBound = true
			if phone, err := phonecrypto.Decrypt(a.PhoneKey, stat.PhoneEnc); err == nil {
				rows[i].Phone = phone
			}
		}
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
		// 完整 11 位手机号走哈希等值匹配；其余情况按昵称或 openid 模糊匹配。
		// 两条分支互斥，否则一个手机号既当哈希又当 LIKE 片段，永远查不到。
		if phonecrypto.Valid(q.Keyword) {
			query = query.Where("phone_hash = ?", phonecrypto.Hash(q.Keyword))
		} else {
			pattern := likePattern(q.Keyword)
			query = query.Where("nickname LIKE ? ESCAPE '!' OR openid LIKE ? ESCAPE '!' OR unionid LIKE ? ESCAPE '!'", pattern, pattern, pattern)
		}
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	// 默认按最近登录倒序：客服找人的第一诉求是"这个人最近来过吗"，
	// 而不是"最早注册的是谁"。从未登录过的用户 last_login_at 为 NULL，排在最后。
	err := query.Order("last_login_at DESC, id DESC").
		Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out).Error
	return out, total, err
}

// ChargeUserStats 一次性取回一批用户的手机号密文、钱包余额与订单聚合，避免按行查库。
func (s ResourceStore) ChargeUserStats(ctx context.Context, ids []uint64) (map[uint64]chargeUserStat, error) {
	stats := make(map[uint64]chargeUserStat, len(ids))

	var users []struct {
		ID       uint64 `gorm:"column:id"`
		PhoneEnc []byte `gorm:"column:phone_enc"`
	}
	if err := s.UserDB.WithContext(ctx).Raw(
		"SELECT id, phone_enc FROM user WHERE id IN ?", ids).Scan(&users).Error; err != nil {
		return nil, err
	}
	for _, u := range users {
		stat := stats[u.ID]
		stat.PhoneEnc = u.PhoneEnc
		stats[u.ID] = stat
	}

	// 一个用户理论上只有一个未删除的钱包行，但用 LEFT JOIN 而不是假定唯一，
	// 免得历史数据里出现两条时静默丢一条。
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

	// 累计消费只认已计费的订单：未计费或已取消的订单 total_cents 可能为 NULL，
	// COALESCE 成 0 后再求和，与"还没产生费用"的口径一致。
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
	// 站点 ID 不在 charge_order 上，要经 charge_payment_intent 关联——该表对
	// charge_order_id 建了唯一键，一单至多一条意图，LEFT JOIN 不会放大行数。
	// 没走过支付流程的早期订单没有对应意图，station_id 留 null。
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

	// 券与报障只取计数，不取明细：客服要判断的是"这人是不是薅券薅得多"，
	// 具体是哪几张券在订单页里按 user_id 查更合适。
	//
	// 扫进一个只含三个计数的小结构体，不能直接 Scan(&detail)：ChargeUserDetail 里
	// 带着 RecentOrders 切片，GORM 会把它当成关联字段去解析，然后以"未定义外键"
	// 拒绝整条查询——报错发生在解析阶段，跟 SQL 对不对无关。
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
