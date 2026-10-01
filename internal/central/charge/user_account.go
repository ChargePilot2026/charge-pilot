package charge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/phone"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserAccountAPI 提供钱包、优惠券、发票、公告、附近站点、手机号绑定及用户故障上报接口。
type UserAccountAPI struct {
	Auth             identity.SessionAuthenticator
	UserDB           *gorm.DB
	AdminDB          *gorm.DB
	GatewayURL       string
	ServiceToken     string
	Gateway          serviceclient.Client
	Prepay           PrepayProvider
	DevelopmentPhone bool
	PhoneExchange    func(context.Context, string) (string, error)
}

// Register 注册用户账户路由；各处理器先验证会话，缺失或无效时返回 401。
func (a UserAccountAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/wallet/balance", a.walletBalance)
	r.GET("/api/v1/user/wallet/txns", a.walletTxns)
	r.GET("/api/v1/user/wallet/recharges", a.walletRecharges)
	r.POST("/api/v1/user/wallet/recharge", a.walletRecharge)
	r.GET("/api/v1/user/wallet/refunds", a.walletRefunds)
	r.POST("/api/v1/user/wallet/refund", a.walletRefund)
	r.GET("/api/v1/user/coupon/my", a.myCoupons)
	r.GET("/api/v1/user/announcement/list", a.announcements)
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
	available := max(wallet.BalanceCents-wallet.FrozenCents, 0)
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
		query = query.Where("direction = ?", direction)
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

// Wallet requests use owner-scoped UUIDs. The payment and frozen funds are
// recorded atomically; channel calls can be retried against the same order.
func (a UserAccountAPI) walletRecharges(c *gin.Context) {
	user, ok := a.userID(c)
	if !ok {
		return
	}
	page, size, ok := readPaging(c)
	if !ok {
		return
	}
	q := a.UserDB.WithContext(c.Request.Context()).Table("wallet_recharge_request r").Where("r.user_id=?", user)
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		resourceWriteFailure(c, err)
		return
	}
	rows := []struct {
		RequestID   string     `json:"request_id"`
		AmountCents int64      `json:"amount_cents"`
		Status      string     `json:"status"`
		ExpiredAt   *time.Time `json:"expires_at"`
		CreatedAt   time.Time  `json:"created_at"`
		CanPay      bool       `json:"can_pay" gorm:"-"`
	}{}
	if err := q.Select("r.request_id,r.amount_cents,r.created_at,p.status,p.expired_at").Joins("JOIN payment_order p ON p.id=r.payment_order_id").Order("r.created_at DESC,r.request_id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		resourceWriteFailure(c, err)
		return
	}
	for i := range rows {
		rows[i].CanPay = rows[i].Status == "initiated" && rows[i].ExpiredAt != nil && rows[i].ExpiredAt.After(time.Now())
	}
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": rows, "total": total, "page": page, "page_size": size})
}
func (a UserAccountAPI) walletRecharge(c *gin.Context) {
	user, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`
		AmountCents int64  `json:"amount_cents"`
	}
	if c.ShouldBindJSON(&in) != nil || uuid.Validate(in.RequestID) != nil || in.AmountCents < 100 || in.AmountCents > 5000000 {
		httpapi.BadRequest(c, "充值金额须在 1 元至 50000 元之间，并提供有效请求号")
		return
	}
	ctx := c.Request.Context()
	var openid string
	if err := a.UserDB.WithContext(ctx).Table("user").Where("id=? AND deleted_at IS NULL", user).Pluck("openid", &openid).Error; err != nil || openid == "" {
		httpapi.Write(c, 409, 2009, "账号身份不可用", nil)
		return
	}
	var order PaymentOrderRecord
	err := a.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize all wallet requests for one owner, including simultaneous retries.
		var owner struct{ ID uint64 }
		if err := tx.Table("user").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", user).Take(&owner).Error; err != nil {
			return err
		}
		var wallet struct {
			ID     uint64
			Status string
		}
		err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", user).Take(&wallet).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = tx.Table("wallet_account").Create(map[string]any{"user_id": user, "balance_cents": 0, "frozen_cents": 0, "status": "active"}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if wallet.Status != "active" {
			return errWalletFrozen
		}
		var prior struct {
			UserID         uint64
			AmountCents    int64
			PaymentOrderID uint64
		}
		err = tx.Table("wallet_recharge_request").Where("request_id=?", in.RequestID).Take(&prior).Error
		if err == nil {
			if prior.UserID != user || prior.AmountCents != in.AmountCents {
				return ErrPaymentIntentConflict
			}
			return tx.Where("id=?", prior.PaymentOrderID).Take(&order).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().UTC()
		orderNo, err := newPaymentOrderNumber(tx)
		if err != nil {
			return err
		}
		order = PaymentOrderRecord{OrderNo: orderNo, BizType: "wallet_recharge", UserID: user, PayMethod: "wechat", TotalCents: in.AmountCents, Status: "initiated", ExpiredAt: sql.NullTime{Time: now.Add(30 * time.Minute), Valid: true}, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		return tx.Table("wallet_recharge_request").Create(map[string]any{"request_id": in.RequestID, "user_id": user, "amount_cents": in.AmountCents, "payment_order_id": order.ID, "request_json": mustJSON(in)}).Error
	})
	if errors.Is(err, ErrPaymentIntentConflict) || errors.Is(err, errWalletFrozen) {
		httpapi.Write(c, 409, 2009, "请求号冲突或钱包已冻结", nil)
		return
	}
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	canPay := order.Status == "initiated" && order.ExpiredAt.Valid && order.ExpiredAt.Time.After(time.Now())
	response := gin.H{"request_id": in.RequestID, "payment_order_id": order.ID, "merchant_order_no": order.OrderNo, "amount_cents": order.TotalCents, "status": order.Status, "can_pay": canPay}
	if canPay {
		var stored ChargePrepayRecord
		var params payment.PrepayParams
		err := a.UserDB.WithContext(ctx).Where("payment_order_id=?", order.ID).Take(&stored).Error
		if err == nil {
			err = json.Unmarshal(stored.ParamsJSON, &params)
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			if a.Prepay == nil {
				httpapi.Write(c, 503, 5003, "支付渠道未配置", nil)
				return
			}
			params, err = a.Prepay.Prepay(ctx, payment.PrepayRequest{MerchantOrderNo: order.OrderNo, OpenID: openid, AmountCents: order.TotalCents, ExpiresAt: order.ExpiredAt.Time})
			if err == nil {
				encoded, _ := json.Marshal(params)
				stored = ChargePrepayRecord{PaymentOrderID: order.ID, ParamsJSON: encoded, PrepayID: sql.NullString{String: params.PrepayID, Valid: params.PrepayID != ""}}
				err = a.UserDB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&stored).Error
				if err == nil {
					err = a.UserDB.WithContext(ctx).Where("payment_order_id=?", order.ID).Take(&stored).Error
				}
				if err == nil {
					err = json.Unmarshal(stored.ParamsJSON, &params)
				}
			}
		}
		if err != nil {
			httpapi.Write(c, 503, 5003, "支付渠道暂不可用，请重试同一充值单", nil)
			return
		}
		response["payment_params"] = params
	}
	httpapi.OK(c, response)
}

func (a UserAccountAPI) walletRefund(c *gin.Context) {
	user, ok := a.userID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`
		AmountCents int64  `json:"amount_cents"`
		Reason      string `json:"reason"`
	}
	if c.ShouldBindJSON(&in) != nil || uuid.Validate(in.RequestID) != nil || in.AmountCents <= 0 || in.AmountCents > 5000000 || len([]rune(in.Reason)) > 255 {
		httpapi.BadRequest(c, "退款金额、原因或请求号无效")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	var response map[string]any
	err := a.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var wallet struct {
			ID                        uint64
			BalanceCents, FrozenCents int64
			Status                    string
		}
		if err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", user).Take(&wallet).Error; err != nil {
			return err
		}
		var prior struct {
			UserID       uint64
			AmountCents  int64
			Reason       *string
			ResponseJSON string
		}
		err := tx.Table("wallet_refund_request").Where("request_id=?", in.RequestID).Take(&prior).Error
		if err == nil {
			reason := ""
			if prior.Reason != nil {
				reason = *prior.Reason
			}
			if prior.UserID != user || prior.AmountCents != in.AmountCents || reason != in.Reason {
				return ErrRefundConflict
			}
			return json.Unmarshal([]byte(prior.ResponseJSON), &response)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if wallet.Status != "active" {
			return errWalletFrozen
		}
		if wallet.BalanceCents-wallet.FrozenCents < in.AmountCents {
			return ErrInsufficientBalance
		}
		if err := tx.Table("wallet_refund_request").Create(map[string]any{"request_id": in.RequestID, "user_id": user, "amount_cents": in.AmountCents, "reason": in.Reason}).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Table("wallet_refund_request").Where("user_id=? AND created_at>=?", user, time.Now().UTC().Add(-5*time.Minute)).Count(&count).Error; err != nil {
			return err
		}
		status := "accepted"
		if count >= 3 {
			status = "manual_review"
			if err := tx.Table("risk_freeze_log").Create(map[string]any{"user_id": user, "trigger_rule": "wallet_refund_frequency", "frozen_action": "wallet"}).Error; err != nil {
				return err
			}
			var id uint64
			if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
				return err
			}
			if err := tx.Table("wallet_risk_freeze_link").Create(map[string]any{"request_id": in.RequestID, "freeze_id": id}).Error; err != nil {
				return err
			}
			if err := tx.Table("wallet_account").Where("id=?", wallet.ID).Updates(map[string]any{"status": "frozen", "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		} else if err := ReserveWalletRefund(tx, WalletRefundReservation{RequestID: in.RequestID, UserID: user, AmountCents: in.AmountCents, Reason: in.Reason}); err != nil {
			return err
		}
		response = map[string]any{"request_id": in.RequestID, "status": status, "refund_cents": in.AmountCents}
		b, _ := json.Marshal(response)
		return tx.Table("wallet_refund_request").Where("request_id=?", in.RequestID).Update("response_json", string(b)).Error
	})
	switch {
	case errors.Is(err, ErrInsufficientBalance):
		httpapi.Write(c, 409, 2009, "可用余额不足", nil)
	case errors.Is(err, ErrRefundConflict):
		httpapi.Write(c, 409, 2009, "申请冲突或原充值可退额度不足", nil)
	case errors.Is(err, errWalletFrozen):
		httpapi.Write(c, 409, 2009, "钱包已冻结，请等待审核", nil)
	case err != nil:
		resourceWriteFailure(c, err)
	default:
		httpapi.OK(c, response)
	}
}

func (a UserAccountAPI) walletRefunds(c *gin.Context) {
	user, ok := a.userID(c)
	if !ok {
		return
	}
	page, size, ok := readPaging(c)
	if !ok {
		return
	}
	q := a.UserDB.WithContext(c.Request.Context()).Table("wallet_refund_request").Where("user_id=?", user)
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		resourceWriteFailure(c, err)
		return
	}
	rows := []struct {
		RequestID   string
		AmountCents int64
		Reason      *string
		CreatedAt   time.Time
	}{}
	if err := q.Order("created_at DESC,request_id").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		resourceWriteFailure(c, err)
		return
	}
	items := []gin.H{}
	for _, row := range rows {
		parts := []struct {
			RefundNo      string     `json:"refund_no"`
			RefundCents   int64      `json:"refund_cents"`
			Status        string     `json:"status"`
			FailureReason *string    `json:"failure_reason"`
			CompletedAt   *time.Time `json:"completed_at"`
		}{}
		if err := a.UserDB.WithContext(c.Request.Context()).Table("wallet_refund_part p").Select("r.refund_no,r.refund_cents,r.status,r.failure_reason,r.completed_at").Joins("JOIN refund_record r ON r.id=p.refund_record_id").Where("p.request_id=?", row.RequestID).Order("r.id").Find(&parts).Error; err != nil {
			resourceWriteFailure(c, err)
			return
		}
		status := "pending"
		var refunded int64
		for _, part := range parts {
			switch part.Status {
			case "success":
				refunded += part.RefundCents
			case "failed", "needs_review":
				status = "needs_review"
			case "processing":
				if status != "needs_review" {
					status = "processing"
				}
			}
		}
		if refunded == row.AmountCents {
			status = "success"
		}
		var review struct {
			Approved bool   `json:"approved"`
			Comment  string `json:"comment"`
			ActorID  uint64 `json:"actor_id"`
		}
		reviewErr := a.UserDB.WithContext(c.Request.Context()).Table("wallet_risk_review").Where("request_id=?", row.RequestID).Take(&review).Error
		if reviewErr != nil && !errors.Is(reviewErr, gorm.ErrRecordNotFound) {
			resourceWriteFailure(c, reviewErr)
			return
		}
		var linked int64
		if err := a.UserDB.WithContext(c.Request.Context()).Table("wallet_risk_freeze_link").Where("request_id=?", row.RequestID).Count(&linked).Error; err != nil {
			resourceWriteFailure(c, err)
			return
		}
		if linked > 0 && errors.Is(reviewErr, gorm.ErrRecordNotFound) {
			status = "manual_review"
		} else if reviewErr == nil && !review.Approved {
			status = "rejected"
		}
		item := gin.H{"request_id": row.RequestID, "amount_cents": row.AmountCents, "refunded_cents": refunded, "status": status, "reason": row.Reason, "created_at": row.CreatedAt, "refund_orders": parts}
		if reviewErr == nil {
			item["review"] = review
		}
		items = append(items, item)
	}
	httpapi.OK(c, gin.H{"user_id": strconv.FormatUint(user, 10), "items": items, "total": total, "page": page, "page_size": size})
}

// ---- 优惠券 ----

func (a UserAccountAPI) myCoupons(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	page, size, ok := readPaging(c)
	if !ok {
		return
	}
	status := c.Query("status")
	if status != "" && !oneOfStatus(status, "unused used expired") {
		httpapi.BadRequest(c, "优惠券状态无效")
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
		if row.Status == "unused" && !row.ExpiresAt.After(now) {
			row.Status = "expired"
		}
		if status != "" && row.Status != status {
			continue
		}
		usable := row.Status == "unused" && row.ExpiresAt.After(now)
		if onlyUsable && !usable {
			continue
		}
		out = append(out, gin.H{
			"coupon_id": row.CouponID, "name": row.Name, "grant_id": row.GrantID,
			"discount_type": row.Type, "discount_value_cents": row.Amount,
			"discount_percent": row.Percent, "min_charge_cents": row.MinCharge,
			"status": row.Status, "expired_at": row.ExpiresAt, "expires_at": row.ExpiresAt, "usable": usable,
		})
	}
	total := len(out)
	start := min((page-1)*size, total)
	end := min(start+size, total)
	httpapi.OK(c, gin.H{"items": out[start:end], "count": total, "total": total, "page": page, "page_size": size})
}

// ---- 公告 ----

func (a UserAccountAPI) announcements(c *gin.Context) {
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()
	// 返回有效期内的全局公告，以及当前用户可达站点范围内的公告。
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

// ---- 站点 ----

// stationRow 是站点列表的查询投影，坐标统一转为字符串以保留库中 DECIMAL 的原始精度。
// DistanceKM 只在按距离查询时填充；未定位分支下保持 nil。
type stationRow struct {
	ID         uint64   `gorm:"column:id"`
	Name       string   `gorm:"column:name"`
	Address    *string  `gorm:"column:address"`
	Longitude  string   `gorm:"column:longitude"`
	Latitude   string   `gorm:"column:latitude"`
	Status     string   `gorm:"column:status"`
	Phone      *string  `gorm:"column:contact_phone"`
	DistanceKM *float64 `gorm:"column:distance_km"`
}

func (a UserAccountAPI) nearbyStations(c *gin.Context) {
	longitude, latitude, hasCoordinates, ok := readCoordinates(c)
	if !ok {
		return
	}
	// 未开启定位时按创建时间倒序返回最新站点，默认 10 条；
	// 已定位时按距离返回，默认条数与其它列表接口一致。
	pageSizeDefault := 20
	if !hasCoordinates {
		pageSizeDefault = 10
	}
	page, pageSize, ok := readPagingDefault(c, pageSizeDefault)
	if !ok {
		return
	}
	radius := 50.0
	if raw := c.Query("radius_km"); raw != "" {
		var err error
		radius, err = strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(radius) || math.IsInf(radius, 0) || radius < 1 || radius > 50 {
			httpapi.BadRequest(c, "搜索半径须为1至50公里")
			return
		}
	}
	ctx := c.Request.Context()
	if !hasCoordinates {
		// 未开启定位时无法计算距离，distance_km 固定为 null，
		// 调用方须按“距离未知”展示，不得按 0 公里处理。
		rows := []stationRow{}
		err := a.AdminDB.WithContext(ctx).Table("station").
			Select("id, name, address, CAST(longitude AS CHAR) AS longitude, CAST(latitude AS CHAR) AS latitude, status, contact_phone").
			Where("deleted_at IS NULL AND status = 'active'").
			Order("created_at DESC, id DESC").
			Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error
		if err != nil {
			httpapi.Write(c, 503, 5003, "站点暂时无法读取", nil)
			return
		}
		items := make([]gin.H, 0, len(rows))
		for _, row := range rows {
			items = append(items, gin.H{
				"id": row.ID, "name": row.Name, "address": row.Address,
				"longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status,
				"contact_phone": row.Phone, "distance_km": nil,
			})
		}
		httpapi.OK(c, gin.H{"items": items, "page": page, "page_size": pageSize, "located": false})
		return
	}
	// 距离由 SQL 直接基于库里的 DECIMAL 坐标算出，
	// 这样排序和分页都发生在结果被截断之前。
	rows := []stationRow{}
	err := a.AdminDB.WithContext(ctx).Raw(`
		SELECT * FROM (SELECT id, name, address,
		       CAST(longitude AS CHAR) AS longitude, CAST(latitude AS CHAR) AS latitude,
		       status, contact_phone,
		       ROUND(6371 * ACOS(LEAST(1, COS(RADIANS(?)) * COS(RADIANS(latitude)) * COS(RADIANS(longitude) - RADIANS(?))
		         + SIN(RADIANS(?)) * SIN(RADIANS(latitude)))), 3) AS distance_km
		FROM station
		WHERE deleted_at IS NULL AND status = 'active') nearby WHERE distance_km <= ?
		ORDER BY distance_km ASC, id ASC
		LIMIT ? OFFSET ?`,
		latitude, longitude, latitude, radius, pageSize, (page-1)*pageSize).Scan(&rows).Error
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
			"contact_phone": row.Phone, "distance_km": row.DistanceKM,
		})
	}
	httpapi.OK(c, gin.H{"items": items, "page": page, "page_size": pageSize, "located": true})
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
		Phone     *string `gorm:"column:contact_phone"`
	}
	err = a.AdminDB.WithContext(c.Request.Context()).Table("station").
		Select("id, name, address, CAST(longitude AS CHAR) AS longitude, CAST(latitude AS CHAR) AS latitude, status, contact_phone").
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
	ctx := c.Request.Context()
	announcements, err := a.stationAnnouncements(ctx, row.ID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "站点公告暂时无法读取", nil)
		return
	}
	devices, err := a.stationDevices(ctx, row.ID)
	if err != nil {
		httpapi.Write(c, 503, 5003, "站点设备暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"id": row.ID, "name": row.Name, "address": row.Address,
		"longitude": row.Longitude, "latitude": row.Latitude, "status": row.Status,
		"contact_phone": row.Phone,
		"announcements": announcements, "devices": devices,
	})
}

// deviceOnlineWindow 是判定设备在线的心跳有效期。超过该窗口即视为离线。
const deviceOnlineWindow = time.Hour

// stationAnnouncements 返回对指定站点生效的公告：全部全局公告，加上把该站点
// 列入 target_ids 的站点维度公告。城市维度公告需要用户位置才能判定归属，
// 在站点详情这一无位置上下文中不做猜测，故不返回。
func (a UserAccountAPI) stationAnnouncements(ctx context.Context, stationID uint64) ([]gin.H, error) {
	now := time.Now().UTC()
	var rows []struct {
		ID      uint64     `gorm:"column:id"`
		Title   string     `gorm:"column:title"`
		Content string     `gorm:"column:content"`
		Scope   string     `gorm:"column:scope"`
		RawIDs  *string    `gorm:"column:target_ids"`
		StartAt time.Time  `gorm:"column:start_at"`
		EndAt   *time.Time `gorm:"column:end_at"`
	}
	if err := a.AdminDB.WithContext(ctx).Table("announcement").
		Where("deleted_at IS NULL AND status = 'published' AND scope IN ('global','station') AND start_at <= ? AND (end_at IS NULL OR end_at >= ?)", now, now).
		Order("start_at DESC, id DESC").Limit(50).Find(&rows).Error; err != nil {
		return nil, err
	}
	items := []gin.H{}
	for _, row := range rows {
		if row.Scope == "station" && !announcementTargets(row.RawIDs)[strconv.FormatUint(stationID, 10)] {
			continue
		}
		items = append(items, gin.H{"id": row.ID, "title": row.Title, "content": row.Content, "start_at": row.StartAt, "end_at": row.EndAt})
	}
	return items, nil
}

// announcementTargets 把 target_ids 的 JSON 字符串数组解析成集合。
// 后台按字符串写入 id，这里保持同样的比较口径；解析失败时返回空集合。
func announcementTargets(raw *string) map[string]bool {
	targets := map[string]bool{}
	if raw == nil {
		return targets
	}
	var ids []string
	if err := json.Unmarshal([]byte(*raw), &ids); err != nil {
		return targets
	}
	for _, id := range ids {
		targets[id] = true
	}
	return targets
}

// stationDevices 返回站点下的设备及其在线状态。设备清单来自 central 的
// device_meta 冗余表，运行态心跳再向 gateway 批量补齐。
// 在线判定只依据最后心跳时间是否落在 deviceOnlineWindow 内；
// 网关不可用或设备从未上报时分别用 runtime_available 和 last_heartbeat_at
// 表达，不把未知状态当作离线。
func (a UserAccountAPI) stationDevices(ctx context.Context, stationID uint64) ([]gin.H, error) {
	var rows []struct {
		DeviceID string `gorm:"column:device_id"`
		Status   string `gorm:"column:status"`
	}
	if err := a.AdminDB.WithContext(ctx).Table("device_meta").
		Select("device_id, status").
		Where("station_id = ? AND deleted_at IS NULL AND status <> 'retired'", stationID).
		Order("device_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]gin.H, 0, len(rows))
	heartbeats := a.deviceHeartbeats(ctx, rows)
	now := time.Now().UTC()
	for _, row := range rows {
		summary, known := heartbeats[row.DeviceID]
		last := summary.LastHeartbeatAt
		online := known && last != nil && now.Sub(*last) < deviceOnlineWindow
		items = append(items, gin.H{
			"device_id": row.DeviceID, "status": row.Status,
			"last_heartbeat_at": last, "online": online, "runtime_available": known,
		})
	}
	return items, nil
}

// deviceHeartbeats 批量取回设备最后心跳。网关未配置或调用失败时返回空集合，
// 调用方据此把设备标为状态未知，而不是离线。
func (a UserAccountAPI) deviceHeartbeats(ctx context.Context, rows []struct {
	DeviceID string `gorm:"column:device_id"`
	Status   string `gorm:"column:status"`
}) map[string]struct {
	DeviceID        string     `json:"device_id"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
} {
	result := map[string]struct {
		DeviceID        string     `json:"device_id"`
		LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	}{}
	if len(rows) == 0 || a.GatewayURL == "" || a.ServiceToken == "" {
		return result
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.DeviceID)
	}
	query := url.Values{}
	for _, id := range ids {
		query.Add("device_id", id)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				DeviceID        string     `json:"device_id"`
				LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := (serviceclient.Client{Timeout: 5 * time.Second}).GetJSON(ctx, a.GatewayURL, a.ServiceToken, "/api/v1/internal/device-summaries?"+query.Encode(), &response); err != nil || response.Code != 0 {
		return result
	}
	for _, item := range response.Data.Items {
		result[item.DeviceID] = item
	}
	return result
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
	if !a.DevelopmentPhone {
		if in.Code == "" || in.Phone != "" {
			httpapi.BadRequest(c, "请使用微信手机号授权凭证")
			return
		}
		if a.PhoneExchange == nil {
			httpapi.Write(c, 503, 5003, "手机号授权暂不可用", nil)
			return
		}
		phone, err := a.PhoneExchange(c.Request.Context(), in.Code)
		if err != nil {
			httpapi.Write(c, 400, 1004, "手机号授权失败，请重新授权", nil)
			return
		}
		in.Phone = phone
	}
	in.Phone = phone.Normalize(in.Phone)
	if !phone.Valid(in.Phone) {
		httpapi.BadRequest(c, "请输入有效的中国大陆手机号")
		return
	}
	err := a.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// 查询提供明确的冲突提示，唯一索引兜底并发绑定。
		var holder int64
		if err := tx.Table("user").Where("phone = ? AND id <> ? AND deleted_at IS NULL", in.Phone, userID).Count(&holder).Error; err != nil {
			return err
		}
		if holder > 0 {
			return errPhoneTaken
		}
		return tx.Table("user").Where("id = ? AND deleted_at IS NULL", userID).
			Updates(map[string]any{"phone": in.Phone}).Error
	})
	var duplicate *mysql.MySQLError
	switch {
	case errors.Is(err, errPhoneTaken) || errors.As(err, &duplicate) && duplicate.Number == 1062:
		httpapi.Write(c, 409, 2009, "该手机号已绑定其他账号", nil)
	case err != nil:
		resourceWriteFailure(c, err)
	default:
		httpapi.OK(c, gin.H{"bound": true, "phone_masked": phone.Mask(in.Phone)})
	}
}

func (a UserAccountAPI) unbindPhone(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	err := a.UserDB.WithContext(c.Request.Context()).Table("user").
		Where("id = ? AND deleted_at IS NULL", userID).
		Updates(map[string]any{"phone": nil}).Error
	if err != nil {
		resourceWriteFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"bound": false, "unbound": true})
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
		// 新报障尚未指派运营人员，assigned_to 为必填字段，初始记录提交用户。
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
	// 仅已结算的充电订单可申请发票。
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
	return readPagingDefault(c, 20)
}

// readPagingDefault 在未显式传入 page_size 时使用 fallback 作为每页条数，
// 供同一接口在不同查询模式下需要不同默认值的场景使用。
func readPagingDefault(c *gin.Context, fallback int) (int, int, bool) {
	page, size := 1, fallback
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

// readCoordinates 解析可选经纬度。两个参数都缺省时 present 为 false，调用方据此走
// 未定位分支；只提供其一、格式错误或越界都按请求错误处理。
func readCoordinates(c *gin.Context) (float64, float64, bool, bool) {
	rawLon, rawLat := c.Query("longitude"), c.Query("latitude")
	if rawLon == "" && rawLat == "" {
		return 0, 0, false, true
	}
	if rawLon == "" || rawLat == "" {
		httpapi.BadRequest(c, "经纬度需同时提供")
		return 0, 0, false, false
	}
	longitude, errLon := strconv.ParseFloat(rawLon, 64)
	latitude, errLat := strconv.ParseFloat(rawLat, 64)
	if errLon != nil || errLat != nil {
		httpapi.BadRequest(c, "经纬度格式无效")
		return 0, 0, false, false
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || math.IsNaN(latitude) || math.IsInf(latitude, 0) ||
		longitude < -180 || longitude > 180 || latitude < -90 || latitude > 90 {
		httpapi.BadRequest(c, "经纬度超出有效范围")
		return 0, 0, false, false
	}
	return longitude, latitude, true, true
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
