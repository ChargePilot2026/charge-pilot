package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/google/uuid"
)

func demoScheme() pricing.Scheme {
	return pricing.Scheme{Name: demoPrefix + "完整充电方案", Remark: "开发示例，设备能力需实测核验后才能启动",
		Amount:   &pricing.AmountMode{Algorithm: pricing.ModeServerRealtimePower, Periods: []pricing.Period{{EndMinute: 1440, Tiers: []pricing.Tier{{MaxWatts: 200, ElectricCents: 80, ServiceCents: 40}, {MaxWatts: 9990, ElectricCents: 160, ServiceCents: 80}}}}},
		Energy:   &pricing.EnergyMode{ElectricCents: 80, ServiceCents: 20},
		Packages: []pricing.Package{{ID: 1, Name: "3元金额", Mode: "amount", PriceCents: 300}, {ID: 2, Name: "120分钟", Mode: "duration", PriceCents: 200, Minutes: 120}, {ID: 3, Name: "3度", Mode: "energy", PriceCents: 300, KWh: 3}},
		Card:     pricing.CardPolicy{PackageID: 2, MaxMinutes: 600}, Display: pricing.DefaultDisplay()}.Normalized()
}

func seedStationScheme(ctx context.Context, db *sql.DB, station int64) (int64, error) {
	s := demoScheme()
	if err := s.Validate(); err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(s.SpecFor(s.Packages[0]))
	display, _ := json.Marshal(s.Display)
	template, err := db.ExecContext(ctx, "INSERT INTO pricing_template(name,spec_json,display_json,status,version) VALUES(?,?,?,'active',1)", s.Name, string(raw), string(display))
	if err != nil {
		return 0, err
	}
	id, _ := template.LastInsertId()
	rule, err := db.ExecContext(ctx, "INSERT INTO pricing_rule(name,station_id,template_id,spec_json,status,version,channel) VALUES(?,?,?,?,'active',1,'default')", s.Name, station, id, string(raw))
	if err != nil {
		return 0, err
	}
	return rule.LastInsertId()
}

// These are historical synthetic receipts, never requests sent to a real device.
// They use a complete frozen duration scheme with actual payment relationships.
func seedOrderContract(ctx context.Context, db *sql.DB, id, user int64, u demoUser, o demoOrder, created time.Time, device string, station, ruleID int64, orderNo string) error {
	price, minutes := o.cents, uint16(60)
	if price <= 0 {
		price = 200
	}
	if o.status == "refunded" {
		price *= 2
		minutes = 120
	}
	s := pricing.Scheme{Name: demoPrefix + "历史时长方案", Packages: []pricing.Package{{ID: 1, Name: fmt.Sprintf("%d分钟", minutes), Mode: "duration", PriceCents: price, Minutes: minutes}}, Display: pricing.DefaultDisplay()}.Normalized()
	rule := pricing.Rule{ID: uint64(ruleID), StationID: uint64(station), Version: 1, Spec: s.SpecFor(s.Packages[0])}
	offer := s.Offers(rule)[0]
	snapshot, _ := json.Marshal(map[string]any{"rule": rule, "offer": offer})
	intent, merchant := uuid.NewString(), fmt.Sprintf("DEMO-MERCHANT-%d", id)
	paid, refunded, status := price, int64(0), "paid"
	if o.status == "cancelled" {
		paid, status = 0, "closed"
	}
	if o.status == "refunded" {
		refunded, status = price-o.cents, "partial_refunded"
	}
	return withSeedTransaction(ctx, db, func(tx *sql.Tx) error {
		payment, err := tx.ExecContext(ctx, "INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,refunded_cents,status,created_month) VALUES(?,'charge',?,?,'balance',?,?,?,?,?)", merchant, id, user, price, paid, refunded, status, created.Format("2006-01")+"-01")
		if err != nil {
			return err
		}
		pid, _ := payment.LastInsertId()
		port := device + ":1"
		if _, err := tx.ExecContext(ctx, "UPDATE charge_order SET payment_order_id=?,charge_mode=0,charge_quantity=?,port_code=? WHERE id=?", pid, minutes, port, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO charge_payment_intent(intent_id,client_request_id,merchant_order_no,payment_order_id,user_id,openid,device_id,port_no,port_code,station_id,pricing_rule_id,pricing_rule_version,pricing_snapshot,estimated_kwh,estimated_minutes,total_cents,charge_mode,charge_quantity,status,expires_at,paid_at,created_at,charge_order_id,offer_id) VALUES(?,?,?,?,?,?,?,1,?,?,?,1,?,'0.000',?,?,0,?,?,?,?,?,?,?)`, intent, uuid.NewString(), merchant, pid, user, u.openid, device, port, station, ruleID, string(snapshot), minutes, price, minutes, intentStatus(o.status), created.Add(15*time.Minute), created, created, id, offer.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO charge_order_pricing(charge_order_id,payment_intent_id,user_id,port_code,pricing_snapshot) VALUES(?,?,?,?,?)", id, intent, user, port, string(snapshot)); err != nil {
			return err
		}
		if !orderHasEndedAt(o.status) {
			return nil
		}
		kwh, _ := strconv.ParseFloat(o.kwh, 64)
		source := billing.Source{ChargeOrderID: uint64(id), OrderNo: orderNo, UserID: uint64(user), Rule: rule, Offer: &offer, Meter: pricing.ActualMeter{StartedAt: created, EndedAt: created.Add(time.Hour), ChargedSeconds: 3600, ChargedWh: uint32(kwh * 1000)}}
		fee, err := billing.PriceSource(source)
		if err != nil {
			return err
		}
		result := billing.Result{CalculationNo: fmt.Sprintf("FEE%020d", id), Source: source, ActualFee: fee}
		raw, _ := json.Marshal(result)
		_, err = tx.ExecContext(ctx, "INSERT INTO charge_fee_receipt(charge_order_id,calculation_no,result_json,shortfall_cents) VALUES(?,?,?,0)", id, result.CalculationNo, string(raw))
		return err
	})
}

func withSeedTransaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
