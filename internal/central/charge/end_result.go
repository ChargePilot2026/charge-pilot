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

	chargedb "github.com/ChargePilot2026/charge-pilot/internal/central/charge/generated"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
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

type EndResultStore struct{ DB *sql.DB }

func (s EndResultStore) Apply(ctx context.Context, result EndResult) (bool, error) {
	if s.DB == nil || result.OrderNo == "" || result.ChargeOrderID == 0 || result.StartCommandID == "" || result.StopCommandID == "" || result.DeviceID == "" || result.PortNo == 0 || result.PortID == 0 || result.PortID > math.MaxInt64 || result.Meter.ChargedSeconds > math.MaxInt32 || result.Meter.EndedAt.IsZero() {
		return false, ErrEndResultConflict
	}
	result.Meter.EndedAt = result.Meter.EndedAt.UTC().Truncate(time.Millisecond)
	meterJSON, err := json.Marshal(result.Meter)
	if err != nil {
		return false, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	q := chargedb.New(tx)
	order, err := q.LockOrderForEnd(ctx, chargedb.LockOrderForEndParams{ID: result.ChargeOrderID, OrderNo: result.OrderNo})
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrEndResultConflict
	}
	if err != nil {
		return false, err
	}
	if order.DeviceID != result.DeviceID || order.PortNo != result.PortNo {
		return false, ErrEndResultConflict
	}
	existing, err := q.EndReceiptByOrder(ctx, result.ChargeOrderID)
	if err == nil {
		var previous EndMeter
		if json.Unmarshal(existing.MeterJson, &previous) != nil || existing.StopCommandID != result.StopCommandID || previous.ChargedWh != result.Meter.ChargedWh || previous.ChargedSeconds != result.Meter.ChargedSeconds || previous.StopReason != result.Meter.StopReason || !previous.EndedAt.Equal(result.Meter.EndedAt) {
			return false, ErrEndResultConflict
		}
		return true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if order.Status != chargedb.ChargeOrderStatusCharging || !order.StartedAt.Valid || result.Meter.EndedAt.Before(order.StartedAt.Time) {
		return false, ErrEndResultConflict
	}
	start, err := q.StartReceiptByOrder(ctx, result.ChargeOrderID)
	if err != nil || !start.Success || start.CommandID != result.StartCommandID || !start.PortID.Valid || start.PortID.Int64 != int64(result.PortID) {
		return false, ErrEndResultConflict
	}
	if err := q.InsertEndReceipt(ctx, chargedb.InsertEndReceiptParams{ChargeOrderID: result.ChargeOrderID, StopCommandID: result.StopCommandID, MeterJson: meterJSON}); err != nil {
		return false, err
	}
	changed, err := q.MarkOrderCompleted(ctx, chargedb.MarkOrderCompletedParams{EndedAt: sql.NullTime{Time: result.Meter.EndedAt, Valid: true}, ChargedKwh: decimal.NewFromInt(int64(result.Meter.ChargedWh)).Shift(-3).StringFixed(4), ChargedSeconds: sql.NullInt32{Int32: int32(result.Meter.ChargedSeconds), Valid: true}, ID: result.ChargeOrderID, OrderNo: result.OrderNo})
	if err != nil {
		return false, err
	}
	count, err := changed.RowsAffected()
	if err != nil || count != 1 {
		return false, ErrEndResultConflict
	}
	port, err := q.DeleteActivePortAfterEnd(ctx, chargedb.DeleteActivePortAfterEndParams{PortID: result.PortID, DeviceID: result.DeviceID, PortNo: result.PortNo, ChargeOrderID: result.ChargeOrderID})
	if err != nil {
		return false, err
	}
	count, err = port.RowsAffected()
	if err != nil || count != 1 {
		return false, ErrEndResultConflict
	}
	if err := q.InsertStartEvent(ctx, chargedb.InsertStartEventParams{ChargeOrderID: result.ChargeOrderID, EventID: result.StopCommandID, Event: "charge_ended", Detail: "device settlement frame accepted", OccurredAt: result.Meter.EndedAt}); err != nil {
		return false, err
	}
	envelope, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	if err := q.InsertStartOutbox(ctx, chargedb.InsertStartOutboxParams{EventID: result.StopCommandID, Stream: "charge_ended_stream", EnvelopeJson: envelope}); err != nil {
		return false, err
	}
	return false, tx.Commit()
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
