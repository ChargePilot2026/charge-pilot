package admin

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// OrderView 是后台订单列表与详情共用的行模型。
//
// 数据主体是 user_db 的充电订单，并联出支付单与支付意图：后台真正要回答的是
// "这笔订单现在什么状态、钱收没收、退没退"，所以退款状态是拼出来的而不是订单自身的状态。
type OrderView struct {
	Live              *charge.LiveMeterView `json:"live,omitempty" gorm:"-"`
	LiveUnavailable   string                `json:"live_unavailable,omitempty" gorm:"-"`
	OrderID           uint64                `json:"order_id"`                          // 充电订单主键
	OrderNo           string                `json:"order_no"`                          // 业务订单号，对外展示和排障都用它
	UserID            uint64                `json:"user_id"`                           // 下单用户 ID
	DeviceID          string                `json:"device_id"`                         // 设备 ID
	PortNo            uint8                 `json:"port_no"`                           // 充电枪序号
	StationID         *uint64               `json:"station_id"`                        // 所属站点 ID；指针，关联不上时为 null
	StationName       *string               `json:"station_name" gorm:"-"`             // 站点名称；gorm:"-" 表示不参与扫描，由 orderStations 二次回填
	Status            string                `json:"status"`                            // 订单状态：pending_payment/paid/charging/completed/cancelled/failed/refunding/refunded
	CreatedAt         time.Time             `json:"created_at"`                        // 订单创建时间
	StartedAt         *time.Time            `json:"started_at"`                        // 实际开始充电时间；指针，未开始为 null
	EndedAt           *time.Time            `json:"ended_at"`                          // 结束充电时间；指针，未结束为 null
	DurationSeconds   *uint64               `json:"duration_seconds"`                  // 计费时长（秒）；指针，计费完成后才有
	MeterKWh          *string               `json:"meter_kwh" gorm:"column:meter_kwh"` // 电表读数（kWh）；用字符串承载 DECIMAL，避免浮点丢精度
	ElectricFeeCents  *int64                `json:"electric_fee_cents"`                // 电费（分）；指针，尚未计费为 null
	ServiceFeeCents   *int64                `json:"service_fee_cents"`                 // 服务费（分）；指针，尚未计费为 null
	TotalFeeCents     *int64                `json:"total_fee_cents"`                   // 应收合计（分）；指针，尚未计费为 null
	RefundStatus      string                `json:"refund_status"`                     // 退款进度：none/processing/partial_refunded/refunded，由 SQL 依据支付单与退款单算出
	PaymentOrderID    *uint64               `json:"payment_order_id"`                  // 关联支付单主键；指针，未发起支付为 null
	PaymentOrderNo    *string               `json:"payment_order_no"`                  // 支付单号；指针，未发起支付为 null
	PaymentStatus     *string               `json:"payment_status"`                    // 支付单状态；指针，未发起支付为 null
	PaidCents         *int64                `json:"paid_cents"`                        // 实收金额（分）；指针，未支付为 null
	RefundedCents     *int64                `json:"refunded_cents"`                    // 已退金额（分）；指针，未退过为 null
	FailureReason     *string               `json:"failure_reason"`                    // 失败原因；指针，非失败单为 null
	RefundApplicantID *string               `json:"refund_applicant_id" gorm:"-"`      // 发起退款的操作人 ID；gorm:"-" 表示不来自库表，只有具备 order.refund.create 权限时才回填，前端据此显示退款入口
	Billing           *OrderBilling         `json:"billing" gorm:"-"`                  // 分账信息；gorm:"-" 表示不在本查询中加载，仅详情接口填充
}

// OrderBilling 是订单详情附带的结算视图。订单本身不含金额去向，金额怎么分在
// billing_db，需要按订单号二次查询后挂在这里。
type OrderBilling struct {
	CalculationNo *string          `json:"calculation_no"` // 计费单号；指针，尚未完成计费为 null
	Settlements   []SettlementView `json:"settlements"`    // 该订单产生的分账汇总列表
}

// SettlementView 是 billing_db 中一条分账汇总，映射自 settlement 表。
type SettlementView struct {
	SettlementID   uint64      `json:"settlement_id" gorm:"column:id"`                  // 分账汇总主键（表主键列名是 id）
	SettlementNo   string      `json:"settlement_no" gorm:"column:settlement_no"`       // 分账单号
	Mode           string      `json:"mode" gorm:"column:mode"`                         // 分账模式：mode_a / mode_b
	Status         string      `json:"status" gorm:"column:status"`                     // 分账状态：pending/confirmed/paid/failed
	SplitPoolCents int64       `json:"split_pool_cents" gorm:"column:split_pool_cents"` // 进入分账池的金额（分）
	Parties        []PartyView `json:"parties" gorm:"-"`                                // 各参与方金额；gorm:"-" 表示不参与扫描，由调用方逐条回填
}

// PartyView 是分账中的一个参与方及其应得金额，映射自 billing_db.settlement_party_amount。
// 名称与比例都是结算当时的快照，之后改分账模板不会回溯这张表。
type PartyView struct {
	PartyID     uint64 `json:"party_id" gorm:"column:party_id"`         // 参与方 ID
	PartyCode   string `json:"party_code" gorm:"column:party_code"`     // 参与方编码
	PartyName   string `json:"party_name" gorm:"column:party_name"`     // 参与方名称（快照）
	RatioBP     uint32 `json:"ratio_bp" gorm:"column:ratio_bp"`         // 分成比例，万分比：10000 表示 100%
	AmountCents int64  `json:"amount_cents" gorm:"column:amount_cents"` // 该参与方应得金额（分）
	Status      string `json:"status" gorm:"column:status"`             // 打款状态：pending/paid/failed
}

// OrderQuery 是订单列表的查询条件。除 PageQuery 外的字段都是可选筛选项，零值表示不过滤。
type OrderQuery struct {
	PageQuery                    // 复用通用分页与状态白名单校验
	OrderNo, DeviceID string     // 按订单号 / 设备号精确匹配
	StationID         uint64     // 按站点过滤，0 表示不限
	From, To          *time.Time // 充电时间区间（闭区间）；指针，某一端为 nil 表示该端不限
}

// orderColumns 是订单列表与详情共用的查询列，两处口径必须一致。
// refund_status 不存在于任何一张表里，是按支付单状态和退款单状态在 SQL 里现算的。
const orderColumns = `c.id AS order_id,c.order_no,c.user_id,c.device_id,c.port_no,i.station_id,c.status,c.created_at,c.started_at,c.ended_at,
 c.charged_seconds AS duration_seconds,c.charged_kwh AS meter_kwh,c.electric_cents AS electric_fee_cents,c.service_cents AS service_fee_cents,c.total_cents AS total_fee_cents,
 c.payment_order_id,p.order_no AS payment_order_no,p.status AS payment_status,p.paid_cents,p.refunded_cents,c.failure_reason,
 CASE WHEN p.status='refunded' THEN 'refunded' WHEN p.refunded_cents>0 THEN 'partial_refunded' WHEN c.status='refunding' OR EXISTS (SELECT 1 FROM refund_record r WHERE r.payment_order_id=p.id AND r.status IN ('pending','processing') AND r.deleted_at IS NULL) THEN 'processing' ELSE 'none' END AS refund_status`

// orderQuery 返回订单查询的公共部分：联支付单取钱的状态，联支付意图取所属站点，
// 并统一排除软删除。Select 和 Where 由调用方自己补。
func (s ResourceStore) orderQuery(ctx context.Context) *gorm.DB {
	return s.UserDB.WithContext(ctx).Table("charge_order AS c").Joins("LEFT JOIN payment_order AS p ON p.id=c.payment_order_id AND p.user_id=c.user_id AND p.deleted_at IS NULL").Joins("LEFT JOIN charge_payment_intent AS i ON i.charge_order_id=c.id").Where("c.deleted_at IS NULL")
}

// Orders 分页查询订单列表：先 Count 出总数再按创建时间倒序取当页，
// 最后回填站点名称。时间筛选用 COALESCE(started_at， created_at)，未开始充电的订单也能落进区间。
func (s ResourceStore) Orders(ctx context.Context, q OrderQuery) (Page[OrderView], error) {
	out := Page[OrderView]{Items: []OrderView{}, Page: q.Page, PageSize: q.PageSize}
	query := s.orderQuery(ctx)
	if q.OrderNo != "" {
		query = query.Where("c.order_no = ?", q.OrderNo)
	}
	if q.DeviceID != "" {
		query = query.Where("c.device_id = ?", q.DeviceID)
	}
	if q.Status != "" {
		query = query.Where("c.status = ?", q.Status)
	}
	if q.StationID != 0 {
		query = query.Where("i.station_id = ?", q.StationID)
	}
	if q.From != nil {
		query = query.Where("COALESCE(c.started_at,c.created_at) >= ?", q.From.UTC())
	}
	if q.To != nil {
		query = query.Where("COALESCE(c.started_at,c.created_at) <= ?", q.To.UTC())
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	if err := query.Select(orderColumns).Order("c.created_at DESC,c.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error; err != nil {
		return out, err
	}
	return out, s.orderStations(ctx, out.Items)
}

// orderStations 按订单行里的站点 ID 批量补上站点名称。
// 站点名在 admin_db、订单在 user_db，两个库之间不做 JOIN，只能取回后在内存里回填；
// 查不到已删除站点的行保留 nil，不报错。
func (s ResourceStore) orderStations(ctx context.Context, rows []OrderView) error {
	ids := []uint64{}
	for _, r := range rows {
		if r.StationID != nil {
			ids = append(ids, *r.StationID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var stations []Station
	if err := s.AdminDB.WithContext(ctx).Select("id,name").Where("id IN ? AND deleted_at IS NULL", ids).Find(&stations).Error; err != nil {
		return err
	}
	names := map[uint64]string{}
	for _, r := range stations {
		names[r.ID] = r.Name
	}
	for i := range rows {
		if rows[i].StationID != nil {
			if name, ok := names[*rows[i].StationID]; ok {
				rows[i].StationName = &name
			}
		}
	}
	return nil
}

// orders 是 GET /api/v1/admin/orders 的处理函数：校验并组装查询条件后返回分页结果。
// 订单号与设备号限长 64，站点 ID 必须是正整数，时间须为 ISO 8601 且开始不晚于结束。
func (a ResourceAPI) orders(c *gin.Context) {
	page, ok := parsePage(c, "pending_payment paid charging completed cancelled failed refunding refunded")
	if !ok {
		return
	}
	q := OrderQuery{PageQuery: page, OrderNo: c.Query("order_no"), DeviceID: c.Query("device_id")}
	if len(q.OrderNo) > 64 || len(q.DeviceID) > 64 {
		httpapi.BadRequest(c, "订单号或设备号过长")
		return
	}
	if raw := c.Query("station_id"); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			httpapi.BadRequest(c, "站点 ID 无效")
			return
		}
		q.StationID = id
	}
	for key, target := range map[string]**time.Time{"started_from": &q.From, "started_to": &q.To} {
		if raw := c.Query(key); raw != "" {
			v, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				httpapi.BadRequest(c, "时间格式须为 ISO 8601")
				return
			}
			*target = &v
		}
	}
	if q.From != nil && q.To != nil && q.From.After(*q.To) {
		httpapi.BadRequest(c, "开始时间不能晚于结束时间")
		return
	}
	out, err := a.Store.Orders(c.Request.Context(), q)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.orderLive(c.Request.Context(), out.Items)
	httpapi.OK(c, out)
}

// order 是 GET /api/v1/admin/orders/：id 的处理函数：返回订单详情，并补上
// billing_db 里的计费单号、分账汇总和各参与方金额。
// RefundApplicantID 只有当操作人持有 order.refund.create 权限时才回填，
// 前端据此显示退款入口；它不参与后端退款权限校验，只影响界面是否展示按钮。
func (a ResourceAPI) order(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var row OrderView
	if err := a.Store.orderQuery(ctx).Select(orderColumns).Where("c.id = ?", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rows := []OrderView{row}
	if err := a.Store.orderStations(ctx, rows); err != nil {
		resourceFailure(c, err)
		return
	}
	row = rows[0]
	a.orderLive(ctx, rows)
	row = rows[0]
	// 匿名结构只为承载这一张事件表的五个可读列，避免为一个查询单独定义类型。
	billing := OrderBilling{Settlements: []SettlementView{}}
	// calculation_no 可空：计费完成前还没有计费单。
	var fee struct{ CalculationNo *string }
	if err := a.Store.BillingDB.WithContext(ctx).Table("fee_receipt").Select("calculation_no").Where("charge_order_id = ?", id).Limit(1).Scan(&fee).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	billing.CalculationNo = fee.CalculationNo
	if err := a.Store.BillingDB.WithContext(ctx).Table("settlement").Where("order_no = ?", row.OrderNo).Order("id").Find(&billing.Settlements).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	for i := range billing.Settlements {
		billing.Settlements[i].Parties = []PartyView{}
		if err := a.Store.BillingDB.WithContext(ctx).Table("settlement_party_amount").Where("settlement_id = ?", billing.Settlements[i].SettlementID).Order("id").Find(&billing.Settlements[i].Parties).Error; err != nil {
			resourceFailure(c, err)
			return
		}
	}
	row.Billing = &billing
	for _, p := range c.MustGet("admin_profile").(Profile).Permissions {
		if p == "order.refund.create" {
			actor := strconv.FormatUint(c.MustGet("admin_profile").(Profile).ID, 10)
			row.RefundApplicantID = &actor
		}
	}
	httpapi.OK(c, row)
}

// timeline 是 GET /api/v1/admin/orders/：id/timeline 的处理函数：
// 按发生时间顺序返回该订单的事件日志，用于还原一次充电从下单到结束的完整过程。
// 先确认订单存在再查日志，避免对不存在的订单返回空列表。
func (a ResourceAPI) timeline(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var order struct{ ID uint64 }
	if err := a.Store.UserDB.WithContext(ctx).Table("charge_order").Select("id").Where("id = ? AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rows := []struct {
		EventID string    `json:"event_id"`
		At      time.Time `json:"at" gorm:"column:occurred_at"`
		Event   string    `json:"event"`
		Actor   string    `json:"actor"`
		Detail  string    `json:"detail"`
	}{}
	if err := a.Store.UserDB.WithContext(ctx).Table("charge_event_log").Where("charge_order_id = ?", id).Order("occurred_at,id").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"order_id": id, "timeline": rows})
}
