package charge

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/phonecrypto"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserAccountAPI 提供小程序需要、
// 而服务此前从未暴露过的客户侧账户视图：钱包、
// 优惠券、发票、公告、客服入口、附近站点、手机号绑定和自助故障上报。
type UserAccountAPI struct {
	Auth         identity.SessionAuthenticator
	UserDB       *gorm.DB
	AdminDB      *gorm.DB
	GatewayURL   string
	ServiceToken string
	Gateway      serviceclient.Client
	Prepay       PrepayProvider
	// PhoneKey 用来加密落库的手机号。
	// 手机号不能以明文躺在库里，
	// 而且每个要读它们的部署都必须提供同一把密钥。
	PhoneKey []byte
}

// Register 挂载账户相关路由。每个处理器都自己做鉴权，
// 这样会话缺失时直接返回 401，而不是带着 user id 为 0 去查库。
func (a UserAccountAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/wallet/balance", a.walletBalance)
	r.GET("/api/v1/user/wallet/txns", a.walletTxns)
	r.GET("/api/v1/user/wallet/recharges", a.walletRecharges)
	r.POST("/api/v1/user/wallet/recharge", a.walletRecharge)
	r.GET("/api/v1/user/wallet/refunds", a.walletRefunds)
	r.POST("/api/v1/user/wallet/refund", a.walletRefund)
	r.GET("/api/v1/user/coupon/my", a.myCoupons)
	r.GET("/api/v1/user/announcement/list", a.announcements)
	r.GET("/api/v1/user/customer-service/entry", a.supportEntry)
	r.GET("/api/v1/user/station/nearby", a.nearbyStations)
	r.GET("/api/v1/user/station/:id", a.stationDetail)
	r.POST("/api/v1/user/phone/bind", a.bindPhone)
	r.POST("/api/v1/user/phone/unbind", a.unbindPhone)
	r.POST("/api/v1/user/device/report-fault", a.reportFault)
	r.GET("/api/v1/user/device/fault-reports", a.myFaultReports)
	r.GET("/api/v1/user/device/fault-reports/:id/history", a.myFaultHistory)
	r.GET("/api/v1/user/invoice/my", a.myInvoices)
	r.POST("/api/v1/user/invoice/apply", a.applyInvoice)
	r.POST("/api/v1/user/charge/:order_id/feedback", a.submitFeedback)
}

func (a UserAccountAPI) userID(c *gin.Context) (uint64, bool) {
	id, ok := a.Auth.Authenticate(c)
	if !ok || id == 0 {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return 0, false
	}
	return id, true
}

// ---- 钱包 ----

func (a UserAccountAPI) walletBalance(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var wallet struct {
		BalanceCents int64  `gorm:"column:balance_cents"`
		FrozenCents  int64  `gorm:"column:frozen_cents"`
		Status       string `gorm:"column:status"`
	}
	err := a.UserDB.WithContext(c.Request.Context()).Table("wallet_account").
		Where("user_id = ? AND deleted_at IS NULL", userID).Take(&wallet).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.OK(c, gin.H{"balance_cents": 0, "frozen_cents": 0, "available_cents": 0, "status": "active"})
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "钱包暂时无法读取", nil)
		return
	}
	available := wallet.BalanceCents - wallet.FrozenCents
	if available < 0 {
		available = 0
	}
	httpapi.OK(c, gin.H{
		"balance_cents": wallet.BalanceCents, "frozen_cents": wallet.FrozenCents,
		"available_cents": available, "status": wallet.Status,
	})
}

func (a UserAccountAPI) walletTxns(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	direction := strings.TrimSpace(c.Query("direction"))
	query := a.UserDB.WithContext(c.Request.Context()).Table("wallet_txn").Where("user_id = ?", userID)
	if direction != "" {
		if direction != "in" && direction != "out" {
			httpapi.BadRequest(c, "收支方向无效")
			return
		}
		if direction == "in" {
			query = query.Where("amount_cents > 0")
		} else {
			query = query.Where("amount_cents < 0")
		}
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		httpapi.Write(c, 503, 5003, "资金流水暂时无法读取", nil)
		return
	}
	rows := []map[string]any{}
	if err := query.Select("txn_no, direction, amount_cents, balance_after_cents, biz_type, biz_ref, note, created_at, created_month").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "资金流水暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

func (a UserAccountAPI) walletRecharges(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	base := a.UserDB.WithContext(c.Request.Context()).Table("wallet_recharge_request").Where("wallet_recharge_request.user_id = ?", userID)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		httpapi.Write(c, 503, 5003, "充值记录暂时无法读取", nil)
		return
	}
	// 请求行本身没有状态，状态在关联的支付订单上。
	rows := []struct {
		RequestID     string    `gorm:"column:request_id" json:"request_id"`
		AmountCents   int64     `gorm:"column:amount_cents" json:"amount_cents"`
		PrepayID      *string   `gorm:"column:prepay_id" json:"-"`
		CreatedAt     time.Time `gorm:"column:created_at" json:"created_at"`
		PaymentStatus *string   `gorm:"column:payment_status" json:"payment_status"`
		PaymentPaid   *int64    `gorm:"column:paid_cents" json:"paid_cents"`
	}{}
	if err := base.Select("wallet_recharge_request.request_id, wallet_recharge_request.amount_cents, wallet_recharge_request.prepay_id, wallet_recharge_request.created_at, p.status AS payment_status, p.paid_cents").
		Joins("LEFT JOIN payment_order AS p ON p.id = wallet_recharge_request.payment_order_id").
		Order("wallet_recharge_request.created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "充值记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

// walletRecharge 开一笔充值订单。
// 金额取自请求但有上下界，
// 同一个请求号会重放已有订单，而不是重复扣一次款。
func (a UserAccountAPI) walletRecharge(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`
		AmountCents int64  `json:"amount_cents"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "充值请求无效")
		return
	}
	if uuid.Validate(in.RequestID) != nil {
		httpapi.BadRequest(c, "请提供 UUID 格式的请求号")
		return
	}
	// 上下界防止一个输错的金额在渠道侧创建出荒谬的订单。
	if in.AmountCents < 100 || in.AmountCents > 5000000 {
		httpapi.BadRequest(c, "充值金额须在 1 元至 50000 元之间")
		return
	}
	ctx := c.Request.Context()
	var identityRow struct {
		OpenID string `gorm:"column:openid"`
	}
	openid := ""
	if err := a.UserDB.WithContext(ctx).Table("user").Select("openid").Where("id = ?", userID).Take(&identityRow).Error; err == nil {
		openid = identityRow.OpenID
	}
	if openid == "" {
		httpapi.Write(c, 409, 2009, "账号缺少微信身份，无法发起充值", nil)
		return
	}
	// 请求行以客户端的 UUID 为键，
	// 重试会复用它，而不是再扣一次款。
	var existing struct {
		RequestID      string  `gorm:"column:request_id"`
		PaymentOrderID *uint64 `gorm:"column:payment_order_id"`
		PrepayID       *string `gorm:"column:prepay_id"`
		AmountCents    int64   `gorm:"column:amount_cents"`
	}
	found := a.UserDB.WithContext(ctx).Table("wallet_recharge_request").Where("request_id = ?", in.RequestID).Take(&existing)
	priorExists := found.Error == nil
	if found.Error != nil && !errors.Is(found.Error, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 503, 5003, "充值记录暂时无法读取", nil)
		return
	}
	if priorExists {
		if existing.AmountCents != in.AmountCents {
			httpapi.Write(c, 409, 2009, "同一请求号不能变更金额", nil)
			return
		}
		if existing.PaymentOrderID != nil {
			httpapi.OK(c, gin.H{"request_id": existing.RequestID, "payment_order_id": *existing.PaymentOrderID, "amount_cents": existing.AmountCents, "replayed": true})
			return
		}
	}
	var paymentOrderID uint64
	err := a.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("payment_order").Create(map[string]any{
			"order_no": rechargeOrderNo(in.RequestID), "biz_type": "wallet_recharge",
			"biz_id": 0, "user_id": userID, "pay_method": "wechat", "total_cents": in.AmountCents,
			"paid_cents": 0, "status": "initiated", "created_month": utcDate(),
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&paymentOrderID).Error; err != nil {
			return err
		}
		// 请求行以客户端 UUID 为键，
		// 更新语句建不出这一行，所以已有行是更新、没有才插入。
		if priorExists {
			return tx.Table("wallet_recharge_request").Where("request_id = ?", in.RequestID).
				Update("payment_order_id", paymentOrderID).Error
		}
		return tx.Table("wallet_recharge_request").Create(map[string]any{
			"request_id": in.RequestID, "user_id": userID, "amount_cents": in.AmountCents,
			"payment_order_id": paymentOrderID, "request_json": mustJSON(map[string]any{"amount_cents": in.AmountCents}),
		}).Error
	})
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	params, err := a.Prepay.Prepay(ctx, payment.PrepayRequest{
		MerchantOrderNo: rechargeOrderNo(in.RequestID),
		OpenID:          openid, AmountCents: in.AmountCents, ExpiresAt: time.Now().Add(30 * time.Minute),
	})
	if err != nil {
		httpapi.Write(c, 503, 5003, "支付渠道暂不可用，请稍后重试", nil)
		return
	}
	_ = a.UserDB.WithContext(ctx).Table("wallet_recharge_request").
		Where("request_id = ?", in.RequestID).Updates(map[string]any{"prepay_id": params.PrepayID}).Error
	httpapi.OK(c, gin.H{"request_id": in.RequestID, "payment_order_id": paymentOrderID, "amount_cents": in.AmountCents, "payment_params": params})
}

// refundReview 是运营对一笔钱包退款申领做出的裁决。
type refundReview struct {
	Status  string  `gorm:"column:status" json:"status"`
	Comment *string `gorm:"column:comment" json:"comment"`
}

func (a UserAccountAPI) walletRefunds(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	rows := []struct {
		RequestID   string        `gorm:"column:request_id" json:"request_id"`
		AmountCents int64         `gorm:"column:amount_cents" json:"amount_cents"`
		Reason      *string       `gorm:"column:reason" json:"reason"`
		CreatedAt   time.Time     `gorm:"column:created_at" json:"created_at"`
		Review      *refundReview `json:"review" gorm:"-"`
	}{}
	// 退款申请本身只是一条申领，
	// 运营的裁决在 wallet_risk_review 行上，所以两者分开读取。
	if err := a.UserDB.WithContext(c.Request.Context()).Table("wallet_refund_request").
		Where("wallet_refund_request.user_id = ?", userID).
		Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "退款记录暂时无法读取", nil)
		return
	}
	if len(rows) > 0 {
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.RequestID)
		}
		var reviews []struct {
			RequestID string  `gorm:"column:request_id"`
			Status    string  `gorm:"column:status"`
			Comment   *string `gorm:"column:comment"`
		}
		if err := a.UserDB.WithContext(c.Request.Context()).Table("wallet_risk_review").
			Where("request_id IN ?", ids).Find(&reviews).Error; err == nil {
			byRequest := map[string]struct {
				Status  string
				Comment *string
			}{}
			for _, review := range reviews {
				byRequest[review.RequestID] = struct {
					Status  string
					Comment *string
				}{review.Status, review.Comment}
			}
			for i := range rows {
				if review, present := byRequest[rows[i].RequestID]; present {
					rows[i].Review = &refundReview{review.Status, review.Comment}
				}
			}
		}
	}
	total := int64(len(rows))
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

// walletRefund 针对钱包余额提交一笔申领。
// 钱立刻被冻结，
// 以免它同时又被拿去充电；真正打款要等运营批准风控复核之后。
func (a UserAccountAPI) walletRefund(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`
		AmountCents int64  `json:"amount_cents"`
		Reason      string `json:"reason"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "退款申请无效")
		return
	}
	if uuid.Validate(in.RequestID) != nil {
		httpapi.BadRequest(c, "请提供 UUID 格式的请求号")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if in.AmountCents <= 0 || in.AmountCents > 5000000 || reason == "" || len([]rune(reason)) > 255 {
		httpapi.BadRequest(c, "退款金额或原因无效")
		return
	}
	ctx := c.Request.Context()
	err := a.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var prior struct {
			RequestID string `gorm:"column:request_id"`
		}
		if found := tx.Table("wallet_refund_request").Where("request_id = ?", in.RequestID).Take(&prior); found.Error == nil {
			return nil
		} else if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		var wallet struct {
			ID           uint64 `gorm:"column:id"`
			BalanceCents int64  `gorm:"column:balance_cents"`
			FrozenCents  int64  `gorm:"column:frozen_cents"`
			Status       string `gorm:"column:status"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("wallet_account").
			Where("user_id = ? AND deleted_at IS NULL", userID).Take(&wallet).Error; err != nil {
			return err
		}
		if wallet.Status != "active" {
			return errWalletFrozen
		}
		if wallet.BalanceCents-wallet.FrozenCents < in.AmountCents {
			return ErrInsufficientBalance
		}
		if err := tx.Table("wallet_account").Where("id = ?", wallet.ID).
			Updates(map[string]any{"frozen_cents": wallet.FrozenCents + in.AmountCents}).Error; err != nil {
			return err
		}
		if err := tx.Table("wallet_refund_request").Create(map[string]any{
			"request_id": in.RequestID, "user_id": userID, "amount_cents": in.AmountCents, "reason": reason,
		}).Error; err != nil {
			return err
		}
		// 分账行以打款记录为键，
		// 所以等运营批准、钱真的出去时才写；
		// 在这里就申领会凭空造出一笔还不存在的退款记录。
		return nil
	})
	switch {
	case errors.Is(err, ErrInsufficientBalance):
		httpapi.Write(c, 409, 2009, "可用余额不足", nil)
	case errors.Is(err, errWalletFrozen):
		httpapi.Write(c, 409, 2009, "钱包已被冻结，无法申请退款", nil)
	case err != nil:
		resourceWriteFailure(c, err)
	default:
		httpapi.OK(c, gin.H{"request_id": in.RequestID, "review_status": "pending", "reserved_cents": in.AmountCents})
	}
}

// ---- 优惠券 ----

func (a UserAccountAPI) myCoupons(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	rows := []CouponGrant{}
	err := a.UserDB.WithContext(c.Request.Context()).Table("coupon_grant AS g").
		Joins("JOIN coupon AS c2 ON c2.id = g.coupon_id AND c2.deleted_at IS NULL AND c2.status = 'active'").
		Where("g.user_id = ? AND g.deleted_at IS NULL", userID).
		Select("g.id, g.coupon_id, g.id AS grant_id, g.status, g.expired_at, c2.name, c2.discount_type, c2.discount_value_cents, c2.discount_percent, c2.min_charge_cents").
		Order("g.status ASC, g.expired_at ASC, g.id ASC").Find(&rows).Error
	if err != nil {
		httpapi.Write(c, 503, 5003, "优惠券暂时无法读取", nil)
		return
	}
	onlyUsable := c.Query("only_usable") == "true"
	now := time.Now().UTC()
	out := []gin.H{}
	for _, row := range rows {
		usable := row.Status == "unused" && row.ExpiresAt.After(now)
		if onlyUsable && !usable {
			continue
		}
		out = append(out, gin.H{
			"coupon_id": row.CouponID, "name": row.Name, "grant_id": row.GrantID,
			"discount_type": row.Type, "discount_value_cents": row.Amount,
			"discount_percent": row.Percent, "min_charge_cents": row.MinCharge,
			"status": row.Status, "expires_at": row.ExpiresAt, "usable": usable,
		})
	}
	httpapi.OK(c, gin.H{"items": out, "count": len(out)})
}

// ---- 公告与客服 ----

func (a UserAccountAPI) announcements(c *gin.Context) {
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()
	// 客户看到的是全局公告，
	// 加上限定在可达站点范围内的那些；不在有效期内的公告一律不返回。
	base := a.AdminDB.WithContext(ctx).Table("announcement").
		Where("deleted_at IS NULL AND status = 'published' AND start_at <= ? AND (end_at IS NULL OR end_at >= ?)", now, now)
	var rows []struct {
		ID      uint64     `gorm:"column:id"`
		Title   string     `gorm:"column:title"`
		Content string     `gorm:"column:content"`
		Scope   string     `gorm:"column:scope"`
		RawIDs  *string    `gorm:"column:target_ids"`
		StartAt time.Time  `gorm:"column:start_at"`
		EndAt   *time.Time `gorm:"column:end_at"`
	}
	if err := base.Order("start_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize * 3).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "公告暂时无法读取", nil)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		if row.Scope == "global" {
			items = append(items, gin.H{"id": row.ID, "title": row.Title, "content": row.Content, "start_at": row.StartAt, "end_at": row.EndAt})
			continue
		}
		// 站点或城市维度的公告仍需做一次归属判断；
		// 只有全局公告在没有位置信息时才无歧义。
		continue
	}
	httpapi.OK(c, gin.H{"items": items, "page": page, "page_size": pageSize})
}

func (a UserAccountAPI) supportEntry(c *gin.Context) {
	if _, ok := a.userID(c); !ok {
		return
	}
	// 小程序渲染的是优先级最高且已启用的那个坐席。
	var seat struct {
		AgentWechat string  `gorm:"column:agent_wechat"`
		AgentName   *string `gorm:"column:agent_name"`
		Path        *string `gorm:"column:path"`
		Priority    uint32  `gorm:"column:priority"`
		Hours       *string `gorm:"column:working_hours_json"`
	}
	err := a.AdminDB.WithContext(c.Request.Context()).Table("customer_service_config").
		Where("enabled = 1").Order("priority ASC, id ASC").Take(&seat).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.OK(c, gin.H{"available": false, "message": "暂未配置在线客服，请通过电话联系我们"})
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "客服入口暂时无法读取", nil)
		return
	}
	payload := gin.H{"available": true, "agent_wechat": seat.AgentWechat, "priority": seat.Priority, "working_hours": seat.Hours}
	if seat.AgentName != nil {
		payload["agent_name"] = *seat.AgentName
	}
	if seat.Path != nil {
		payload["path"] = *seat.Path
	}
	httpapi.OK(c, payload)
}

// ---- 站点 ----

func (a UserAccountAPI) nearbyStations(c *gin.Context) {
	longitude, latitude, ok := readCoordinates(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	type station struct {
		ID         uint64   `gorm:"column:id"`
		Name       string   `gorm:"column:name"`
		Address    *string  `gorm:"column:address"`
		Longitude  string   `gorm:"column:longitude"`
		Latitude   string   `gorm:"column:latitude"`
		Status     string   `gorm:"column:status"`
		OpenHours  *string  `gorm:"column:open_hours"`
		Phone      *string  `gorm:"column:contact_phone"`
		DistanceKM *float64 `gorm:"column:distance_km"`
	}
	// 距离由 SQL 直接基于库里的 DECIMAL 坐标算出，
	// 这样排序和分页都发生在结果被截断之前。
	rows := []station{}
	err := a.AdminDB.WithContext(ctx).Raw(`
		SELECT id, name, address,
		       CAST(longitude AS CHAR) AS longitude, CAST(latitude AS CHAR) AS latitude,
		       status, open_hours, contact_phone,
		       ROUND(6371 * ACOS(LEAST(1, COS(RADIANS(?)) * COS(RADIANS(latitude)) * COS(RADIANS(longitude) - RADIANS(?))
		         + SIN(RADIANS(?)) * SIN(RADIANS(latitude)))), 3) AS distance_km
		FROM station
		WHERE deleted_at IS NULL AND status = 'active'
		ORDER BY distance_km ASC, id ASC
		LIMIT ? OFFSET ?`,
		latitude, longitude, latitude, pageSize*4, (page-1)*pageSize).Scan(&rows).Error
	if err != nil {
		httpapi.Write(c, 503, 5003, "站点暂时无法读取", nil)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		if row.DistanceKM != nil && *row.DistanceKM > 50 {
			continue
		}
		items = append(items, gin.H{
			"id": row.ID, "name": row.Name, "address": row.Address,
			"longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status,
			"open_hours": row.OpenHours, "contact_phone": row.Phone, "distance_km": row.DistanceKM,
		})
	}
	httpapi.OK(c, gin.H{"items": items, "page": page, "page_size": pageSize})
}

func (a UserAccountAPI) stationDetail(c *gin.Context) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "站点 ID 无效")
		return
	}
	var row struct {
		ID        uint64  `gorm:"column:id"`
		Name      string  `gorm:"column:name"`
		Address   *string `gorm:"column:address"`
		Longitude string  `gorm:"column:longitude"`
		Latitude  string  `gorm:"column:latitude"`
		Status    string  `gorm:"column:status"`
		OpenHours *string `gorm:"column:open_hours"`
		Phone     *string `gorm:"column:contact_phone"`
	}
	err = a.AdminDB.WithContext(c.Request.Context()).Table("station").
		Select("id, name, address, CAST(longitude AS CHAR) AS longitude, CAST(latitude AS CHAR) AS latitude, status, open_hours, contact_phone").
		Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "站点不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "站点暂时无法读取", nil)
		return
	}
	if row.Status != "active" {
		httpapi.Write(c, 404, 1004, "站点不存在", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"id": row.ID, "name": row.Name, "address": row.Address,
		"longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status,
		"open_hours": row.OpenHours, "contact_phone": row.Phone,
	})
}

// ---- 手机号 ----

func (a UserAccountAPI) bindPhone(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "手机号请求无效")
		return
	}
	if !phonecrypto.Valid(in.Phone) {
		httpapi.BadRequest(c, "请输入有效的中国大陆手机号")
		return
	}
	if len(a.PhoneKey) == 0 {
		// 拒绝比存下一个平台保护不了的号码安全。
		httpapi.Write(c, 503, 5003, "手机号加密未配置，暂不可绑定", nil)
		return
	}
	hash := phonecrypto.Hash(in.Phone)
	encrypted, err := phonecrypto.Encrypt(a.PhoneKey, in.Phone)
	if err != nil {
		httpapi.Write(c, 503, 5003, "手机号保护失败，请稍后重试", nil)
		return
	}
	err = a.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// 唯一哈希正是防止一个号码挂到两个账号上的那道闸。
		var holder int64
		if err := tx.Table("user").Where("phone_hash = ? AND id <> ? AND deleted_at IS NULL", hash, userID).Count(&holder).Error; err != nil {
			return err
		}
		if holder > 0 {
			return errPhoneTaken
		}
		return tx.Table("user").Where("id = ? AND deleted_at IS NULL", userID).
			Updates(map[string]any{"phone_enc": encrypted, "phone_hash": hash}).Error
	})
	switch {
	case errors.Is(err, errPhoneTaken):
		httpapi.Write(c, 409, 2009, "该手机号已绑定其他账号", nil)
	case err != nil:
		resourceWriteFailure(c, err)
	default:
		httpapi.OK(c, gin.H{"bound": true, "phone_masked": phonecrypto.Mask(in.Phone)})
	}
}

func (a UserAccountAPI) unbindPhone(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	err := a.UserDB.WithContext(c.Request.Context()).Table("user").
		Where("id = ? AND deleted_at IS NULL", userID).
		Updates(map[string]any{"phone_enc": nil, "phone_hash": nil}).Error
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"bound": false})
}

// ---- 报修 ----

func (a UserAccountAPI) reportFault(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		DeviceID    string   `json:"device_id"`
		PortNo      uint8    `json:"port_no"`
		FaultType   string   `json:"fault_type"`
		Description *string  `json:"description"`
		Images      []string `json:"images"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "报修请求无效")
		return
	}
	if in.DeviceID == "" || len(in.DeviceID) > 64 || !oneOfStatus(in.FaultType, "mechanical electrical communication display other") {
		httpapi.BadRequest(c, "设备或故障类型无效")
		return
	}
	if in.Description != nil && len([]rune(*in.Description)) > 2000 {
		httpapi.BadRequest(c, "故障描述过长")
		return
	}
	if len(in.Images) > 9 {
		httpapi.BadRequest(c, "图片最多 9 张")
		return
	}
	images, _ := json.Marshal(in.Images)
	var id uint64
	err := a.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("device_fault_report").Create(map[string]any{
			"device_id": in.DeviceID, "user_id": userID, "report_source": "user",
			"fault_type": in.FaultType, "description": in.Description,
			"images_json": string(images), "status": "open",
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		// assigned_to 是 NOT NULL 且目前还没有运营接手，
		// 所以开单第一步的操作人记为提交的客户本人。
		return tx.Table("device_fault_report_event").Create(map[string]any{
			"report_id": id, "event_type": "reported", "from_status": "", "to_status": "open",
			"actor_id": userID, "assigned_to": userID, "user_visible": true, "note": "用户提交报修",
		}).Error
	})
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": "open"})
}

func (a UserAccountAPI) myFaultReports(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	base := a.UserDB.WithContext(c.Request.Context()).Table("device_fault_report").
		Where("user_id = ? AND deleted_at IS NULL", userID)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		httpapi.Write(c, 503, 5003, "报修记录暂时无法读取", nil)
		return
	}
	rows := []map[string]any{}
	if err := base.Select("id, device_id, fault_type, description, status, resolved_at, created_at").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "报修记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

func (a UserAccountAPI) myFaultHistory(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "报修编号无效")
		return
	}
	var owned int64
	if err := a.UserDB.WithContext(c.Request.Context()).Table("device_fault_report").
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).Count(&owned).Error; err != nil || owned == 0 {
		// 别人的报障单与不存在的报障单返回完全一样。
		httpapi.Write(c, 404, 1004, "报修记录不存在", nil)
		return
	}
	rows := []map[string]any{}
	// 只暴露客户可见的流转步骤；内部分派信息不外泄。
	if err := a.UserDB.WithContext(c.Request.Context()).Table("device_fault_report_event").
		Where("report_id = ? AND user_visible = 1", id).
		Select("event_type, from_status, to_status, note, created_at").
		Order("id ASC").Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "处理进度暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// ---- 发票 ----

func (a UserAccountAPI) myInvoices(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	base := a.UserDB.WithContext(c.Request.Context()).Table("invoice_request").
		Where("user_id = ? AND deleted_at IS NULL", userID)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		httpapi.Write(c, 503, 5003, "发票记录暂时无法读取", nil)
		return
	}
	rows := []map[string]any{}
	if err := base.Select("id, invoice_no, biz_type, biz_id, total_cents, invoice_type, title, tax_no, email, review_status, reject_reason, invoice_url, created_at").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "发票记录暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

// applyInvoice 为客户自己某一笔已结算的订单申请发票。
// 金额取自订单，绝不取自请求。
func (a UserAccountAPI) applyInvoice(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`
		OrderNo     string `json:"order_no"`
		InvoiceType string `json:"invoice_type"`
		Title       string `json:"title"`
		TaxNo       string `json:"tax_no"`
		Email       string `json:"email"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "发票申请无效")
		return
	}
	if uuid.Validate(in.RequestID) != nil || strings.TrimSpace(in.OrderNo) == "" {
		httpapi.BadRequest(c, "请提供 UUID 请求号与订单号")
		return
	}
	// 库里枚举存的是 normal / vat_special；小程序发过来的也是这两个名字。
	if !oneOfStatus(in.InvoiceType, "normal vat_special") {
		httpapi.BadRequest(c, "发票类型无效")
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" || len([]rune(title)) > 128 {
		httpapi.BadRequest(c, "发票抬头无效")
		return
	}
	if len([]rune(in.TaxNo)) > 32 {
		httpapi.BadRequest(c, "税号过长")
		return
	}
	if len(in.Email) > 128 || (in.Email != "" && !strings.Contains(in.Email, "@")) {
		httpapi.BadRequest(c, "邮箱格式无效")
		return
	}
	ctx := c.Request.Context()
	var order struct {
		ID     uint64 `gorm:"column:id"`
		UserID uint64 `gorm:"column:user_id"`
		Status string `gorm:"column:status"`
	}
	err := a.UserDB.WithContext(ctx).Table("charge_order").
		Select("id, user_id, status").
		Where("order_no = ? AND user_id = ? AND deleted_at IS NULL", in.OrderNo, userID).Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "订单不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		return
	}
	// 只有已结算的充电才能开票；
	// 其它状态开出来的是一笔还没收到的钱。
	if order.Status != "completed" && order.Status != "refunding" && order.Status != "refunded" {
		httpapi.Write(c, 409, 2009, "订单尚未完成，暂不能申请发票", nil)
		return
	}
	// 开票金额取自计费回执，
	// 与订单视图同源，绝不取自计费任务从不写入的列。
	receipt := ChargeFeeRecord{}
	if err := a.UserDB.WithContext(ctx).Where("charge_order_id = ?", order.ID).Take(&receipt).Error; err != nil {
		httpapi.Write(c, 409, 2009, "订单尚未计费，暂不能申请发票", nil)
		return
	}
	_, _, totalCents, ok := receipt.Fees()
	if !ok || totalCents <= 0 {
		httpapi.Write(c, 409, 2009, "订单金额为零，暂不能申请发票", nil)
		return
	}
	invoiceNo := "INV" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))
	var id uint64
	err = a.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing struct {
			ID uint64 `gorm:"column:id"`
		}
		if found := tx.Table("invoice_request").Where("invoice_no = ?", invoiceNo).Take(&existing); found.Error == nil {
			id = existing.ID
			return nil
		} else if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		// 一单一张发票，防止客户把同一笔充电重复申领。
		var duplicate int64
		if err := tx.Table("invoice_request").
			Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", order.ID).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errInvoiceDuplicate
		}
		if err := tx.Table("invoice_request").Create(map[string]any{
			"invoice_no": invoiceNo, "user_id": userID, "biz_type": "charge", "biz_id": order.ID,
			"total_cents": totalCents, "invoice_type": in.InvoiceType, "title": title,
			"tax_no": strings.TrimSpace(in.TaxNo), "email": strings.TrimSpace(in.Email), "review_status": "pending",
		}).Error; err != nil {
			return err
		}
		return tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error
	})
	if errors.Is(err, errInvoiceDuplicate) {
		httpapi.Write(c, 409, 2009, "该订单已申请过发票", nil)
		return
	}
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"invoice_no": invoiceNo, "id": id, "review_status": "pending", "total_cents": totalCents})
}

// ---- 共享工具 ----

var (
	ErrInsufficientBalance = errors.New("可用余额不足")
	errWalletFrozen        = errors.New("钱包已冻结")
	errPhoneTaken          = errors.New("手机号已被占用")
	errInvoiceDuplicate    = errors.New("订单已申请发票")
)

func readPaging(c *gin.Context) (int, int, bool) {
	page, size := 1, 20
	if raw := c.Query("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100000 {
			httpapi.BadRequest(c, "分页参数无效")
			return 0, 0, false
		}
		page = parsed
	}
	if raw := c.Query("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			httpapi.BadRequest(c, "分页参数无效：page_size 为 1–100")
			return 0, 0, false
		}
		size = parsed
	}
	return page, size, true
}

func readCoordinates(c *gin.Context) (float64, float64, bool) {
	longitude, errLon := strconv.ParseFloat(c.Query("longitude"), 64)
	latitude, errLat := strconv.ParseFloat(c.Query("latitude"), 64)
	if errLon != nil || errLat != nil {
		httpapi.BadRequest(c, "请提供经纬度")
		return 0, 0, false
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || math.IsNaN(latitude) || math.IsInf(latitude, 0) ||
		longitude < -180 || longitude > 180 || latitude < -90 || latitude > 90 {
		httpapi.BadRequest(c, "经纬度超出有效范围")
		return 0, 0, false
	}
	return longitude, latitude, true
}

// rechargeOrderNo 推导充值的商户订单号。
// PAY 前缀是支付渠道模拟器和微信都要求的形式。
func rechargeOrderNo(requestID string) string {
	return "PAYW" + strings.ToUpper(strings.ReplaceAll(requestID, "-", ""))
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// resourceWriteFailure 把存储错误映射到统一的响应信封。
func resourceWriteFailure(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "记录不存在", nil)
		return
	}
	httpapi.Write(c, 503, 5003, "数据暂时无法保存，请稍后重试", nil)
}
