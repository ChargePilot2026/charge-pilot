package control

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
)

type StartService struct {
	Orders  PaidOrderAuthorizer
	Store   store.MySQLSink
	Devices *protocol.Registry
}

// Start never treats a socket write as device success. It commits ownership
// and the command before writing, then leaves the result for a matched B8 ACK.
func (s StartService) Start(ctx context.Context, orderNo string) (store.StartReservation, error) {
	paid, err := s.Orders.PaidOrder(ctx, orderNo)
	if err != nil {
		return store.StartReservation{}, err
	}
	reservation, err := s.Store.ReserveStart(ctx, paid)
	if err != nil {
		return store.StartReservation{}, err
	}
	if reservation.Status != "pending" {
		return reservation, nil
	}
	if _, err := dc589.BuildStart(dc589.StartCommand{Session: reservation.Wire.SessionID, Port: reservation.Wire.Port, OrderBCD: reservation.Wire.OrderBCD, Mode: dc589.ChargeMode(reservation.Wire.Mode), Quantity: reservation.Wire.Quantity}); err != nil {
		return store.StartReservation{}, err
	}
	if !s.Devices.Connected(paid.DeviceID) {
		return reservation, nil
	}
	sent, err := s.Store.MarkStartSent(ctx, reservation.CommandID)
	if err != nil {
		return store.StartReservation{}, err
	}
	if !sent {
		status, err := s.Store.StartStatus(ctx, orderNo)
		reservation.Status = status
		return reservation, err
	}
	reservation.Status = "sent"
	if err := s.Devices.Send(ctx, paid.DeviceID, reservation.Wire); err != nil {
		// A partial TCP write may have reached the device. Persist stopping
		// before attempting the compensating STOP; never resend START here.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if persistErr := s.Store.MarkStartStopping(cleanupCtx, reservation.CommandID, err); persistErr != nil {
			return store.StartReservation{}, persistErr
		}
		_ = s.Devices.Send(cleanupCtx, paid.DeviceID, reservation.StopWire)
		reservation.Status = "stopping"
	}
	return reservation, nil
}

func startStatusCode(err error) int {
	if errors.Is(err, ErrUnpaid) || errors.Is(err, store.ErrPortUnavailable) || errors.Is(err, store.ErrOrderConflict) {
		return http.StatusConflict
	}
	return http.StatusServiceUnavailable
}
