package charge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrEndResultConflict = errors.New("charge end result conflicts with order state")

type EndMeter struct {
	ChargedWh      uint32    `json:"charged_wh"`
	ChargedSeconds uint32    `json:"charged_seconds"`
	EndedAt        time.Time `json:"ended_at"`
	StopReason     uint8     `json:"stop_reason"`
}

type EndResult struct {
	OrderNo        string   `json:"order_no" binding:"required,max=64"`
	ChargeOrderID  uint64   `json:"charge_order_id" binding:"required"`
	StartCommandID string   `json:"start_command_id" binding:"required,uuid"`
	StopCommandID  string   `json:"stop_command_id" binding:"required,uuid"`
	DeviceID       string   `json:"device_id" binding:"required,max=64"`
	PortNo         uint8    `json:"port_no" binding:"required"`
	PortID         uint64   `json:"port_id" binding:"required"`
	Meter          EndMeter `json:"meter" binding:"required"`
}

type EndResultStore struct{ DB *gorm.DB }

func (s EndResultStore) Apply(ctx context.Context, result EndResult) (bool, error) {
	if s.DB == nil || result.OrderNo == "" || result.ChargeOrderID == 0 || result.StartCommandID == "" || result.StopCommandID == "" || result.DeviceID == "" || result.PortNo == 0 || result.PortID == 0 || result.PortID > math.MaxInt64 || result.Meter.ChargedSeconds > math.MaxInt32 || result.Meter.EndedAt.IsZero() {
		return false, ErrEndResultConflict
	}
	result.Meter.EndedAt = result.Meter.EndedAt.UTC().Truncate(time.Millisecond)
	meterJSON, err := json.Marshal(result.Meter)
	if err != nil {
		return false, err
	}
	replayed := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order ChargeOrderRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND order_no = ? AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).Take(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEndResultConflict
		}
		if err != nil {
			return err
		}
		if order.DeviceID != result.DeviceID || order.PortNo != result.PortNo {
			return ErrEndResultConflict
		}
		var existing EndReceiptRecord
		err = tx.Where("charge_order_id = ?", result.ChargeOrderID).Take(&existing).Error
		if err == nil {
			var previous EndMeter
			if json.Unmarshal(existing.MeterJSON, &previous) != nil || existing.StopCommandID != result.StopCommandID || previous.ChargedWh != result.Meter.ChargedWh || previous.ChargedSeconds != result.Meter.ChargedSeconds || previous.StopReason != result.Meter.StopReason || !previous.EndedAt.Equal(result.Meter.EndedAt) {
				return ErrEndResultConflict
			}
			replayed = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if order.Status != "charging" || !order.StartedAt.Valid || result.Meter.EndedAt.Before(order.StartedAt.Time) {
			return ErrEndResultConflict
		}
		var start StartReceiptRecord
		if err := tx.Where("charge_order_id = ?", result.ChargeOrderID).Take(&start).Error; err != nil || !start.Success || start.CommandID != result.StartCommandID || !start.PortID.Valid || start.PortID.Int64 != int64(result.PortID) {
			return ErrEndResultConflict
		}
		if err := tx.Create(&EndReceiptRecord{ChargeOrderID: result.ChargeOrderID, StopCommandID: result.StopCommandID, MeterJSON: meterJSON}).Error; err != nil {
			return err
		}
		kwh := decimal.NewFromInt(int64(result.Meter.ChargedWh)).Shift(-3).StringFixed(4)
		updated := tx.Model(&ChargeOrderRecord{}).Where("id = ? AND order_no = ? AND status = 'charging' AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).
			Updates(map[string]any{"status": "completed", "ended_at": result.Meter.EndedAt, "charged_kwh": kwh, "charged_seconds": result.Meter.ChargedSeconds})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrEndResultConflict
		}
		port := tx.Where("port_id = ? AND device_id = ? AND port_no = ? AND charge_order_id = ?", result.PortID, result.DeviceID, result.PortNo, result.ChargeOrderID).Delete(&ActivePortChargeRecord{})
		if port.Error != nil {
			return port.Error
		}
		if port.RowsAffected != 1 {
			return ErrEndResultConflict
		}
		if err := tx.Create(&ChargeEventLogRecord{ChargeOrderID: result.ChargeOrderID, EventID: result.StopCommandID,
			Event: "charge_ended", Actor: "gateway", Detail: "device settlement frame accepted", OccurredAt: result.Meter.EndedAt}).Error; err != nil {
			return err
		}
		envelope, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return tx.Create(&EventOutboxRecord{EventID: result.StopCommandID, Stream: "charge_ended_stream", EnvelopeJSON: envelope}).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return replayed, err
}

type EndResultAPI struct {
	Store        EndResultStore
	ServiceToken string
}

func (a EndResultAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/charge-orders/:order_no/end-result", a.handle)
}

func (a EndResultAPI) handle(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	var request EndResult
	if c.ShouldBindJSON(&request) != nil || c.Param("order_no") != request.OrderNo {
		httpapi.BadRequest(c, "invalid end result")
		return
	}
	replayed, err := a.Store.Apply(c.Request.Context(), request)
	if errors.Is(err, ErrEndResultConflict) {
		httpapi.Write(c, http.StatusConflict, 2000, "end result conflicts with order", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "end result persistence failed", nil)
		return
	}
	httpapi.OK(c, gin.H{"accepted": true, "replayed": replayed})
}
