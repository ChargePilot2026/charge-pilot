package charge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	chargedb "github.com/ChargePilot2026/charge-pilot/internal/central/charge/generated"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
)

var ErrStartResultConflict = errors.New("charge start result conflicts with order state")

type StartResult struct {
	CommandID     string    `json:"command_id" binding:"required,uuid"`
	ChargeOrderID uint64    `json:"charge_order_id" binding:"required"`
	OrderNo       string    `json:"order_no" binding:"required,max=64"`
	DeviceID      string    `json:"device_id" binding:"required,max=64"`
	PortNo        uint8     `json:"port_no" binding:"required"`
	PortID        uint64    `json:"port_id" binding:"required"`
	Success       bool      `json:"success"`
	ResultCode    uint8     `json:"result_code"`
	OccurredAt    time.Time `json:"occurred_at" binding:"required"`
}

type StartResultStore struct{ DB *sql.DB }

func (s StartResultStore) Apply(ctx context.Context, result StartResult) (bool, error) {
	if s.DB == nil || result.CommandID == "" || result.ChargeOrderID == 0 || result.OrderNo == "" || result.DeviceID == "" || result.PortNo == 0 || result.PortID == 0 || result.PortID > math.MaxInt64 || result.OccurredAt.IsZero() || result.Success != (result.ResultCode == 0) {
		return false, ErrStartResultConflict
	}
	result.OccurredAt = result.OccurredAt.UTC().Truncate(time.Millisecond)
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	q := chargedb.New(tx)
	order, err := q.LockOrderForStartResult(ctx, chargedb.LockOrderForStartResultParams{ID: result.ChargeOrderID, OrderNo: result.OrderNo})
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrStartResultConflict
	}
	if err != nil {
		return false, err
	}
	if order.DeviceID != result.DeviceID || order.PortNo != result.PortNo {
		return false, ErrStartResultConflict
	}
	existing, err := q.GetStartReceipt(ctx, result.CommandID)
	if err == nil {
		if existing.ChargeOrderID != result.ChargeOrderID || existing.OrderNo.String != result.OrderNo || existing.DeviceID.String != result.DeviceID || existing.PortNo.Int16 != int16(result.PortNo) || existing.PortID.Int64 != int64(result.PortID) || existing.Success != result.Success || existing.ResultCode.Int16 != int16(result.ResultCode) || !existing.OccurredAt.Time.Equal(result.OccurredAt.UTC()) {
			return false, ErrStartResultConflict
		}
		return true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if order.Status != chargedb.ChargeOrderStatusPaid {
		return false, ErrStartResultConflict
	}
	if err := q.InsertStartReceipt(ctx, chargedb.InsertStartReceiptParams{
		CommandID: result.CommandID, ChargeOrderID: result.ChargeOrderID,
		OrderNo:    sql.NullString{String: result.OrderNo, Valid: true},
		DeviceID:   sql.NullString{String: result.DeviceID, Valid: true},
		PortNo:     sql.NullInt16{Int16: int16(result.PortNo), Valid: true},
		PortID:     sql.NullInt64{Int64: int64(result.PortID), Valid: true},
		Success:    result.Success,
		ResultCode: sql.NullInt16{Int16: int16(result.ResultCode), Valid: true},
		OccurredAt: sql.NullTime{Time: result.OccurredAt.UTC(), Valid: true},
	}); err != nil {
		var dbErr *mysql.MySQLError
		if errors.As(err, &dbErr) && dbErr.Number == 1062 {
			return false, ErrStartResultConflict
		}
		return false, err
	}
	stream, eventName := "charge_started_stream", "start_acked"
	if result.Success {
		if err := q.InsertActivePort(ctx, chargedb.InsertActivePortParams{PortID: result.PortID, DeviceID: result.DeviceID, PortNo: result.PortNo, ChargeOrderID: result.ChargeOrderID, UserID: order.UserID, StartedAt: result.OccurredAt.UTC()}); err != nil {
			var dbErr *mysql.MySQLError
			if errors.As(err, &dbErr) && dbErr.Number == 1062 {
				return false, ErrStartResultConflict
			}
			return false, err
		}
		changed, err := q.MarkOrderCharging(ctx, chargedb.MarkOrderChargingParams{StartedAt: sql.NullTime{Time: result.OccurredAt.UTC(), Valid: true}, ID: result.ChargeOrderID, OrderNo: result.OrderNo})
		if err != nil {
			return false, err
		}
		count, err := changed.RowsAffected()
		if err != nil || count != 1 {
			return false, ErrStartResultConflict
		}
	} else {
		stream, eventName = "refund_required_stream", "start_rejected"
		changed, err := q.MarkOrderRefundingAfterStartFailure(ctx, chargedb.MarkOrderRefundingAfterStartFailureParams{FailureReason: sql.NullString{String: fmt.Sprintf("device START rejected: %d", result.ResultCode), Valid: true}, ID: result.ChargeOrderID, OrderNo: result.OrderNo})
		if err != nil {
			return false, err
		}
		count, err := changed.RowsAffected()
		if err != nil || count != 1 {
			return false, ErrStartResultConflict
		}
	}
	if err := q.InsertStartEvent(ctx, chargedb.InsertStartEventParams{ChargeOrderID: result.ChargeOrderID, EventID: result.CommandID, Event: eventName, Detail: fmt.Sprintf("device=%s port=%d result=%d", result.DeviceID, result.PortNo, result.ResultCode), OccurredAt: result.OccurredAt.UTC()}); err != nil {
		return false, err
	}
	envelope, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	if err := q.InsertStartOutbox(ctx, chargedb.InsertStartOutboxParams{EventID: result.CommandID, Stream: stream, EnvelopeJson: envelope}); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

type StartResultAPI struct {
	Store        StartResultStore
	ServiceToken string
}

func (a StartResultAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/charge-orders/:order_no/start-result", a.handle)
}

func (a StartResultAPI) handle(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	var request StartResult
	if err := c.ShouldBindJSON(&request); err != nil || c.Param("order_no") != request.OrderNo {
		httpapi.BadRequest(c, "invalid start result")
		return
	}
	replayed, err := a.Store.Apply(c.Request.Context(), request)
	if errors.Is(err, ErrStartResultConflict) {
		httpapi.Write(c, http.StatusConflict, 2000, "start result conflicts with order", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "start result persistence failed", nil)
		return
	}
	httpapi.OK(c, gin.H{"accepted": true, "replayed": replayed})
}
