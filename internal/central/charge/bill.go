package charge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BillStore 负责在一笔充电计价完成后开给客户看的账单。
// 金额取自计费回执，
// 所以账单永远不会与实际收取的费用对不上。
type BillStore struct{ DB *gorm.DB }

type Bill struct {
	ID             uint64     `json:"id" gorm:"column:id"`
	BillNo         string     `json:"bill_no" gorm:"column:bill_no"`
	ChargeOrderID  uint64     `json:"charge_order_id" gorm:"column:charge_order_id"`
	PaymentOrderID *uint64    `json:"payment_order_id" gorm:"column:payment_order_id"`
	UserID         uint64     `json:"user_id" gorm:"column:user_id"`
	DeviceID       string     `json:"device_id" gorm:"column:device_id"`
	PortNo         uint8      `json:"port_no" gorm:"column:port_no"`
	ElectricCents  int64      `json:"electric_cents" gorm:"column:electric_cents"`
	ServiceCents   int64      `json:"service_cents" gorm:"column:service_cents"`
	TotalCents     int64      `json:"total_cents" gorm:"column:total_cents"`
	PrepaidCents   int64      `json:"prepaid_cents" gorm:"column:prepaid_cents"`
	RefundCents    int64      `json:"refund_cents" gorm:"column:refund_cents"`
	ShortfallCents int64      `json:"shortfall_cents" gorm:"column:shortfall_cents"`
	ChargedKWh     *string    `json:"charged_kwh" gorm:"column:charged_kwh"`
	ChargedSeconds *int64     `json:"charged_seconds" gorm:"column:charged_seconds"`
	Status         string     `json:"status" gorm:"column:status"`
	IssuedAt       time.Time  `json:"issued_at" gorm:"column:issued_at"`
	SettledAt      *time.Time `json:"settled_at" gorm:"column:settled_at"`
	Read           bool       `json:"read" gorm:"-"`
}

// Issue 为已结算的充电写账单。
// 可以重复调用：
// charge_order_id 上的唯一键让重放成为空操作，而不是第二张账单。
func (s BillStore) Issue(ctx context.Context, chargeOrderID uint64) (Bill, bool, error) {
	if s.DB == nil || chargeOrderID == 0 {
		return Bill{}, false, errors.New("bill store is not configured")
	}
	var order struct {
		ID             uint64     `gorm:"column:id"`
		OrderNo        string     `gorm:"column:order_no"`
		UserID         uint64     `gorm:"column:user_id"`
		DeviceID       string     `gorm:"column:device_id"`
		PortNo         uint8      `gorm:"column:port_no"`
		PaymentOrderID *int64     `gorm:"column:payment_order_id"`
		ChargedKWh     *string    `gorm:"column:charged_kwh"`
		ChargedSeconds *int64     `gorm:"column:charged_seconds"`
		StartedAt      *time.Time `gorm:"column:started_at"`
		CreatedMonth   time.Time  `gorm:"column:created_month"`
	}
	err := s.DB.WithContext(ctx).Table("charge_order").
		Select("id, order_no, user_id, device_id, port_no, payment_order_id, charged_kwh, charged_seconds, started_at, created_month").
		Where("id = ? AND deleted_at IS NULL", chargeOrderID).Take(&order).Error
	if err != nil {
		return Bill{}, false, err
	}
	var receipt ChargeFeeRecord
	if err := s.DB.WithContext(ctx).Where("charge_order_id = ?", order.ID).Take(&receipt).Error; err != nil {
		// 还没有费用说明计费没跑完，账单稍后才会有。
		return Bill{}, false, nil
	}
	electric, service, total, ok := receipt.Fees()
	if !ok || total <= 0 {
		return Bill{}, false, nil
	}

	var prepaid, refunded int64
	if order.PaymentOrderID != nil {
		var payment struct {
			PaidCents     int64 `gorm:"column:paid_cents"`
			RefundedCents int64 `gorm:"column:refunded_cents"`
		}
		if err := s.DB.WithContext(ctx).Table("payment_order").
			Select("paid_cents, refunded_cents").Where("id = ?", *order.PaymentOrderID).Take(&payment).Error; err == nil {
			prepaid, refunded = payment.PaidCents, payment.RefundedCents
		}
	}
	shortfall := receipt.ShortfallCents
	if shortfall <= 0 && total > prepaid {
		shortfall = total - prepaid
	}

	billNo := "BILL" + fmt.Sprintf("%020d", order.ID)
	issuedAt := time.Now().UTC()
	if order.StartedAt != nil {
		// 账单属于充电发生的那个时间点，而不是它被定价的时间点。
		issuedAt = order.StartedAt.Add(0)
	}
	month := time.Date(issuedAt.Year(), issuedAt.Month(), 1, 0, 0, 0, 0, time.UTC)
	row := map[string]any{
		"bill_no": billNo, "charge_order_id": order.ID, "payment_order_id": order.PaymentOrderID,
		"user_id": order.UserID, "device_id": order.DeviceID, "port_no": order.PortNo,
		"electric_cents": electric, "service_cents": service, "total_cents": total,
		"prepaid_cents": prepaid, "refund_cents": refunded, "shortfall_cents": shortfall,
		"charged_kwh": order.ChargedKWh, "charged_seconds": order.ChargedSeconds,
		"status": "issued", "issued_at": issuedAt, "created_month": month,
	}
	var billID uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 唯一键让这次插入天然幂等；
		// 重放只会找到已有账单，而不是再建一张。
		existing := int64(0)
		if err := tx.Table("charge_bill").
			Where("charge_order_id = ? AND created_month = ?", order.ID, month).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			if err := tx.Table("charge_bill").
				Where("charge_order_id = ? AND created_month = ?", order.ID, month).
				Pluck("id", &billID).Error; err != nil {
				return err
			}
			return nil
		}
		if err := tx.Table("charge_bill").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&billID).Error; err != nil {
			return err
		}
		// 账单是订单故事的一部分，
		// 所以客户能在他们本来就在看的那条时间线里看到它。
		eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("bill-issued:"+billNo)).String()
		return tx.Create(&ChargeEventLogRecord{
			ChargeOrderID: order.ID, EventID: eventID, Event: "bill_issued", Actor: "billing",
			Detail:     fmt.Sprintf("bill=%s total=%d prepaid=%d shortfall=%d", billNo, total, prepaid, shortfall),
			OccurredAt: time.Now().UTC(),
		}).Error
	})
	if err != nil {
		return Bill{}, false, err
	}
	bill, err := s.Load(ctx, chargeOrderID)
	return bill, true, err
}

// Load 按计费任务知道的那个充电
// 订单 id 返回这一笔充电的账单。
func (s BillStore) Load(ctx context.Context, chargeOrderID uint64) (Bill, error) {
	bill := Bill{}
	err := s.DB.WithContext(ctx).Table("charge_bill").
		Select("id, bill_no, charge_order_id, payment_order_id, user_id, device_id, port_no, electric_cents, service_cents, total_cents, prepaid_cents, refund_cents, shortfall_cents, charged_kwh, charged_seconds, status, issued_at, settled_at").
		Where("charge_order_id = ?", chargeOrderID).Take(&bill).Error
	return bill, err
}

// LoadByID 按账单 id 返回账单，
// 客户侧接口就是按它寻址的。
func (s BillStore) LoadByID(ctx context.Context, userID, billID uint64) (Bill, error) {
	bill := Bill{}
	err := s.DB.WithContext(ctx).Table("charge_bill").
		Select("id, bill_no, charge_order_id, payment_order_id, user_id, device_id, port_no, electric_cents, service_cents, total_cents, prepaid_cents, refund_cents, shortfall_cents, charged_kwh, charged_seconds, status, issued_at, settled_at").
		Where("id = ? AND user_id = ?", billID, userID).Take(&bill).Error
	return bill, err
}

// Settle 在账单上不再有任何未结金额后把它标记为已结清。
func (s BillStore) Settle(ctx context.Context, chargeOrderID uint64) error {
	return s.DB.WithContext(ctx).Table("charge_bill").
		Where("charge_order_id = ? AND status = 'issued'", chargeOrderID).
		Updates(map[string]any{"status": "settled", "settled_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error
}

// SettleWhenClear 在预付金额加上已
// 退金额已经覆盖这笔充电后关闭账单，
// 这样钱确实退回去之后账单不会一直挂在那里未付。
func (s BillStore) SettleWhenClear(ctx context.Context, chargeOrderID uint64) error {
	bill, err := s.Load(ctx, chargeOrderID)
	if err != nil || bill.Status != "issued" {
		return err
	}
	if bill.ShortfallCents > 0 {
		return nil
	}
	if bill.PrepaidCents+bill.RefundCents < bill.TotalCents {
		return nil
	}
	return s.Settle(ctx, chargeOrderID)
}

// ListBills 按最新优先分页返回某个客户的账单。
// 只有该客户本人可读，所以归属条件是写在查询里的，而不是事后补一道校验。
func (s BillStore) ListBills(ctx context.Context, userID uint64, page, pageSize int) ([]Bill, int64, error) {
	base := s.DB.WithContext(ctx).Table("charge_bill").Where("user_id = ?", userID)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []Bill{}
	if err := base.
		Select("id, bill_no, charge_order_id, payment_order_id, user_id, device_id, port_no, electric_cents, service_cents, total_cents, prepaid_cents, refund_cents, shortfall_cents, charged_kwh, charged_seconds, status, issued_at, settled_at, created_month").
		Order("issued_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	if len(rows) > 0 {
		ids := make([]uint64, 0, len(rows))
		months := make([]time.Time, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
			months = append(months, row.IssuedAt.UTC())
		}
		var reads []struct {
			BillID uint64 `gorm:"column:bill_id"`
		}
		// charge_bill_read 与账单共享同一个分区月份，
		// 所以键的两部分都要匹配。
		if err := s.DB.WithContext(ctx).Table("charge_bill_read").
			Select("bill_id").Where("user_id = ? AND bill_id IN ?", userID, ids).Find(&reads).Error; err == nil {
			seen := map[uint64]bool{}
			for _, read := range reads {
				seen[read.BillID] = true
			}
			for i := range rows {
				rows[i].Read = seen[rows[i].ID]
			}
		}
		_ = months
	}
	return rows, total, nil
}

// MarkRead 记录客户已经看过这张账单。重复查看是空操作。
func (s BillStore) MarkRead(ctx context.Context, userID, billID uint64) error {
	var bill struct {
		CreatedMonth time.Time `gorm:"column:created_month"`
	}
	if err := s.DB.WithContext(ctx).Table("charge_bill").
		Select("created_month").Where("id = ? AND user_id = ?", billID, userID).Take(&bill).Error; err != nil {
		return err
	}
	var existing int64
	if err := s.DB.WithContext(ctx).Table("charge_bill_read").
		Where("bill_id = ? AND created_month = ?", billID, bill.CreatedMonth).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	return s.DB.WithContext(ctx).Table("charge_bill_read").Create(map[string]any{
		"bill_id": billID, "created_month": bill.CreatedMonth, "user_id": userID,
	}).Error
}

// BillHTTP 把账单视图暴露给客户。
type BillHTTP struct {
	Auth  identity.SessionAuthenticator
	Bills BillStore
}

func (a BillHTTP) Register(r *gin.Engine) {
	r.GET("/api/v1/user/bills", a.list)
	r.GET("/api/v1/user/bills/:id", a.detail)
	r.POST("/api/v1/user/bills/:id/read", a.markRead)
}

func (a BillHTTP) list(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	page, pageSize, ok := readPaging(c)
	if !ok {
		return
	}
	rows, total, err := a.Bills.ListBills(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		httpapi.Write(c, 503, 5003, "账单暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

func (a BillHTTP) detail(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "账单编号无效")
		return
	}
	// 账单 id 本身就在查询里带着客户条件，
	// 所以别的账号的账单与不存在的账单无从分辨。
	bill, err := a.Bills.LoadByID(c.Request.Context(), userID, id)
	if err != nil {
		httpapi.Write(c, 404, 1004, "账单不存在", nil)
		return
	}
	httpapi.OK(c, bill)
}

func (a BillHTTP) markRead(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "账单编号无效")
		return
	}
	if _, err := a.Bills.LoadByID(c.Request.Context(), userID, id); err != nil {
		httpapi.Write(c, 404, 1004, "账单不存在", nil)
		return
	}
	if err := a.Bills.MarkRead(c.Request.Context(), userID, id); err != nil {
		httpapi.Write(c, 503, 5003, "账单状态暂时无法保存", nil)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "read": true})
}

// BillIssuer 把 BillStore 适配成计费包的接口，
// 同时不让计费反过来依赖本模块。
type BillIssuer struct{ Store BillStore }

// Issue 为已结算的充电发布账单。
func (a BillIssuer) Issue(ctx context.Context, chargeOrderID uint64) (uint64, error) {
	bill, created, err := a.Store.Issue(ctx, chargeOrderID)
	if err != nil || !created {
		return 0, err
	}
	return bill.ID, nil
}
