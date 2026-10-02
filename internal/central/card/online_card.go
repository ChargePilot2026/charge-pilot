package card

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	paymentpkg "github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	refundpkg "github.com/ChargePilot2026/charge-pilot/internal/central/refund"
	walletpkg "github.com/ChargePilot2026/charge-pilot/internal/central/wallet"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrCardOperation = errors.New("刷卡不可用：卡状态、余额、端口、累计上限或设备确认状态不满足要求")
var ErrCardBalance = fmt.Errorf("%w：钱包可用余额不足", ErrCardOperation)

type OnlineCard struct {
	ID     uint64 `json:"id"`
	CardNo string `json:"card_no"`
	UserID uint64 `json:"user_id,string"`
	Status string `json:"status"`
}

func (OnlineCard) TableName() string { return "online_card" }

type CardStore struct{ DB *gorm.DB }

// mapWalletMoveError 将 wallet 包账本错误映射回刷卡业务哨兵，保持对调用方的错误语义不变。
func mapWalletMoveError(err error) error {
	if errors.Is(err, walletpkg.ErrInsufficient) {
		return ErrCardBalance
	}
	if errors.Is(err, walletpkg.ErrInactive) {
		return ErrCardOperation
	}
	return err
}

// CanExtend checks the whole purchase, never accepts a partial addition.
func CanExtend(card, orderCard uint64, p pricing.Package, purchased, max uint16, balance int64, pending bool) bool {
	return card == orderCard && p.Mode == "duration" && p.PriceCents > 0 && p.Minutes > 0 && !pending && balance >= p.PriceCents && uint32(purchased)+uint32(p.Minutes) <= uint32(max)
}

// Swipe only consumes events whose hardware identity has been verified by the
// adapter. Transport retries reuse device_id/event_id and the same operation.
func (s CardStore) Swipe(ctx context.Context, cardNo, eventID string, port PortRef, rule pricing.Rule) (CardOperation, error) {
	var out CardOperation
	if n, err := strconv.ParseUint(cardNo, 10, 32); err != nil || n == 0 || strconv.FormatUint(n, 10) != cardNo {
		return out, ErrCardOperation
	}
	if cardNo == "" || eventID == "" || len(eventID) > 128 || !port.Found || !port.Online || port.Kind != "port" {
		return out, ErrCardOperation
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var card OnlineCard
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("card_no=?", cardNo).Take(&card).Error; err != nil {
			return err
		}
		var previous CardOperation
		found := tx.Where("device_id=? AND event_id=?", port.DeviceID, eventID).Find(&previous)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected > 0 {
			if previous.CardID != card.ID {
				return ErrCardOperation
			}
			var session CardCharge
			if err := tx.Where("charge_order_id=? AND port_code=?", previous.ChargeOrderID, port.PortID).Take(&session).Error; err != nil {
				return ErrCardOperation
			}
			out = previous
			return nil
		}
		if card.Status != "active" {
			return ErrCardOperation
		}
		var user struct{ Status string }
		if err := tx.Table("user").Where("id=? AND deleted_at IS NULL", card.UserID).Take(&user).Error; err != nil {
			return err
		}
		if user.Status != "active" {
			return ErrCardOperation
		}
		w, err := walletpkg.Lock(tx, card.UserID)
		if err != nil {
			return err
		}
		var session CardCharge
		active := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("active_port=?", port.PortID).Find(&session)
		if active.Error != nil {
			return active.Error
		}
		var p pricing.Package
		out = CardOperation{OperationID: uuid.NewString(), DeviceID: port.DeviceID, EventID: eventID, CardID: card.ID, Status: "confirming"}
		if active.RowsAffected > 0 {
			if json.Unmarshal(session.PackageJSON, &p) != nil {
				return ErrCardOperation
			}
			var order orderpkg.ChargeOrderRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='charging' AND deleted_at IS NULL", session.ChargeOrderID).Take(&order).Error; err != nil {
				return ErrCardOperation
			}
			if !order.StartedAt.Valid || !time.Now().UTC().Before(order.StartedAt.Time.Add(time.Duration(session.PurchasedMinutes)*time.Minute)) || order.UserID != card.UserID {
				return ErrCardOperation
			}
			var cutoff int64
			if err := tx.Table("charge_billing_cutoff").Where("charge_order_id=?", order.ID).Count(&cutoff).Error; err != nil {
				return err
			}
			if cutoff != 0 {
				return ErrCardOperation
			}
			var pending int64
			if err := tx.Model(&CardOperation{}).Where("charge_order_id=? AND status='confirming'", session.ChargeOrderID).Count(&pending).Error; err != nil {
				return err
			}
			if !CanExtend(card.ID, session.CardID, p, session.PurchasedMinutes, session.MaxMinutes, w.BalanceCents-w.FrozenCents, pending > 0) {
				if CanExtend(card.ID, session.CardID, p, session.PurchasedMinutes, session.MaxMinutes, p.PriceCents, pending > 0) {
					return ErrCardBalance
				}
				return ErrCardOperation
			}
			out.Kind = "extend"
			out.Status = "confirmed"
			out.ChargeOrderID = session.ChargeOrderID
			updated := tx.Model(&paymentpkg.PaymentOrderRecord{}).Where("id=? AND pay_method='balance' AND status IN ('paid','partial_refunded')", order.PaymentOrderID.Int64).Updates(map[string]any{"paid_cents": gorm.Expr("paid_cents+?", p.PriceCents), "total_cents": gorm.Expr("total_cents+?", p.PriceCents)})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCardOperation
			}
			if err := orderpkg.SyncOrderPaymentStatus(tx, uint64(order.PaymentOrderID.Int64)); err != nil {
				return err
			}
			if err := tx.Model(&CardCharge{}).Where("charge_order_id=?", session.ChargeOrderID).Updates(map[string]any{"paid_cents": gorm.Expr("paid_cents+?", p.PriceCents), "purchased_minutes": gorm.Expr("purchased_minutes+?", p.Minutes)}).Error; err != nil {
				return err
			}
		} else {
			if err := paymentpkg.CheckoutPortAvailable(tx, port.PortID); err != nil {
				return err
			}
			if !port.Available || rule.Spec.Scheme == nil || rule.Spec.Scheme.Card.PackageID == 0 {
				return ErrCardOperation
			}
			var ok bool
			p, ok = rule.Spec.Scheme.Package(rule.Spec.Scheme.Card.PackageID)
			if !ok || !CanExtend(card.ID, card.ID, p, 0, rule.Spec.Scheme.Normalized().Card.MaxMinutes, w.BalanceCents-w.FrozenCents, false) {
				if ok && CanExtend(card.ID, card.ID, p, 0, rule.Spec.Scheme.Normalized().Card.MaxMinutes, p.PriceCents, false) {
					return ErrCardBalance
				}
				return ErrCardOperation
			}
			out.Kind = "start"
			spec := rule.Spec.Scheme.SpecFor(p)
			rule.Spec = spec
			var offer pricing.Offer
			for _, o := range spec.Scheme.Offers(rule) {
				if o.PackageID == p.ID {
					offer = o
					offer.ServerDuration = true
				}
			}
			estimate := pricing.Estimate{Mode: pricing.ModeDeviceDuration, EstimatedKWh: "0.000", EstimatedMinutes: p.Minutes, PrepaidCents: p.PriceCents, TotalCents: p.PriceCents, ChargeMode: 4, ChargeQuantity: spec.Scheme.Normalized().Card.MaxMinutes}
			snapshot, _ := json.Marshal(map[string]any{"rule": rule, "offer": offer, "estimate": estimate})
			intentID := uuid.NewString()
			payNo, err := paymentpkg.NewOrderNumber(tx)
			if err != nil {
				return err
			}
			payment := paymentpkg.PaymentOrderRecord{OrderNo: payNo, BizType: "charge", UserID: card.UserID, PayMethod: "balance", TotalCents: p.PriceCents, PaidCents: p.PriceCents, Status: "paid", PaidAt: sql.NullTime{Time: time.Now().UTC(), Valid: true}, CreatedMonth: dbutil.MonthStart()}
			if err := tx.Create(&payment).Error; err != nil {
				return err
			}
			chargeNo, err := orderpkg.NewChargeOrderNumber(tx, port.DeviceID, port.PortNo)
			if err != nil {
				return err
			}
			order := orderpkg.ChargeOrderRecord{OrderNo: chargeNo, UserID: card.UserID, DeviceID: port.DeviceID, PortNo: port.PortNo, PortCode: sql.NullString{String: port.PortID, Valid: true}, PaymentOrderID: sql.NullInt64{Int64: int64(payment.ID), Valid: true}, Status: "paid", PaymentStatus: "paid", ChargeMode: 4, ChargeQuantity: spec.Scheme.Normalized().Card.MaxMinutes, CreatedMonth: dbutil.MonthStart()}
			if err := tx.Create(&order).Error; err != nil {
				return err
			}
			out.ChargeOrderID = order.ID
			if err := tx.Model(&paymentpkg.PaymentOrderRecord{}).Where("id=?", payment.ID).Update("biz_id", order.ID).Error; err != nil {
				return err
			}
			intent := paymentpkg.PaymentIntentRecord{IntentID: intentID, ClientRequestID: out.OperationID, MerchantOrderNo: payNo, PaymentOrderID: payment.ID, UserID: card.UserID, DeviceID: port.DeviceID, PortNo: port.PortNo, PortCode: port.PortID, StationID: port.StationID, PricingRuleID: rule.ID, PricingRuleVersion: rule.Version, PricingSnapshot: snapshot, EstimatedKWh: "0.000", EstimatedMinutes: p.Minutes, TotalCents: p.PriceCents, ChargeMode: 4, ChargeQuantity: spec.Scheme.Normalized().Card.MaxMinutes, Status: "paid", ExpiresAt: time.Now().UTC().Add(5 * time.Minute), ChargeOrderID: sql.NullInt64{Int64: int64(order.ID), Valid: true}}
			if err := tx.Create(&intent).Error; err != nil {
				return err
			}
			if err := tx.Create(&orderpkg.ChargePricingSnapshotRecord{ChargeOrderID: order.ID, PaymentIntentID: intentID, UserID: card.UserID, PortCode: port.PortID, PricingSnapshot: snapshot}).Error; err != nil {
				return err
			}
			praw, _ := json.Marshal(p)
			portCode := port.PortID
			if err := tx.Create(&CardCharge{ChargeOrderID: order.ID, CardID: card.ID, PortCode: portCode, ActivePort: &portCode, PaidCents: p.PriceCents, PurchasedMinutes: p.Minutes, MaxMinutes: spec.Scheme.Normalized().Card.MaxMinutes, PackageJSON: praw, CardNo: card.CardNo, WalletAfterCents: w.BalanceCents - w.FrozenCents - p.PriceCents}).Error; err != nil {
				// 并发刷卡抢同一端口时 active_port 唯一键拒绝后写事务，
				// 按业务冲突处理，设备重试即可。
				if dbutil.IsMySQLDuplicate(err) {
					return ErrCardOperation
				}
				return err
			}
		}
		out.PriceCents, out.Minutes = p.PriceCents, p.Minutes
		if err := mapWalletMoveError(walletpkg.Move(tx, card.UserID, &w, -p.PriceCents, "CP"+out.OperationID, "在线刷卡"+out.Kind)); err != nil {
			return err
		}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		if out.Kind == "extend" {
			if err := tx.Model(&CardOperation{}).Where("operation_id=?", out.OperationID).Update("confirmed_at", gorm.Expr("UTC_TIMESTAMP(3)")).Error; err != nil {
				return err
			}
		}
		return tx.Create(&orderpkg.ChargeEventLogRecord{ChargeOrderID: out.ChargeOrderID, EventID: out.OperationID, Event: "card_" + out.Kind + "_" + out.Status, Actor: "card", Detail: fmt.Sprintf("card=%d cents=%d minutes=%d", card.ID, p.PriceCents, p.Minutes), OccurredAt: time.Now().UTC()}).Error
	})
	return out, err
}

// RefundInTx 是刷卡退款的跨家族编排：钱包入金、payment_order 改写、
// 订单支付状态同步与退款记录创建在同一事务内完成。
// 由结算（刷卡剩余退款）与启动失败退款持有 tx 调用；钱包行须已由调用方按
// 既有加锁顺序（先钱包后支付）锁定。
func RefundInTx(tx *gorm.DB, order orderpkg.ChargeOrderRecord, w *walletpkg.Row, cents int64, refundNo, reason string) error {
	if cents <= 0 {
		return nil
	}
	if err := mapWalletMoveError(walletpkg.Move(tx, order.UserID, w, cents, refundNo, reason)); err != nil {
		return err
	}
	var payment paymentpkg.PaymentOrderRecord
	if err := tx.Where("id=? AND pay_method='balance'", order.PaymentOrderID.Int64).Take(&payment).Error; err != nil {
		return err
	}
	if payment.PaidCents-payment.RefundedCents < cents {
		return ErrCardOperation
	}
	status := "partial_refunded"
	if payment.RefundedCents+cents == payment.PaidCents {
		status = "refunded"
	}
	if err := tx.Model(&paymentpkg.PaymentOrderRecord{}).Where("id=?", payment.ID).Updates(map[string]any{"refunded_cents": gorm.Expr("refunded_cents+?", cents), "status": status}).Error; err != nil {
		return err
	}
	if err := orderpkg.SyncOrderPaymentStatus(tx, payment.ID); err != nil {
		return err
	}
	return tx.Create(&refundpkg.RefundRecord{RefundNo: refundNo, PaymentOrderID: payment.ID, UserID: order.UserID, BizType: "charge", BizID: order.ID, RefundCents: cents, Status: "success", ExecutionPolicy: "automatic", Reason: sql.NullString{String: reason, Valid: true}, CreatedMonth: dbutil.MonthStart()}).Error
}
