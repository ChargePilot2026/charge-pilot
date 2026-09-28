package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	gatewaydb "github.com/ChargePilot2026/charge-pilot/internal/gateway/store/generated"
)

type StopJob struct {
	CommandID string
	OrderNo   string
	DeviceID  string
	PortNo    uint8
	Wire      protocol.Command
}

func (s MySQLSink) CompensateStart(ctx context.Context, orderNo, commandID string) (StopJob, bool, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StopJob{}, false, err
	}
	defer tx.Rollback()
	q := gatewaydb.New(tx)
	row, err := q.LockStartForCompensation(ctx, orderNo)
	if errors.Is(err, sql.ErrNoRows) {
		return StopJob{}, false, ErrOrderConflict
	}
	if err != nil {
		return StopJob{}, false, err
	}
	if row.CommandID != commandID || !row.PortID.Valid || row.PortID.Int64 <= 0 {
		return StopJob{}, false, ErrOrderConflict
	}
	if row.Status == gatewaydb.ChargeCommandStatusPending {
		changed, err := q.MarkPendingStartRejected(ctx, commandID)
		if err != nil {
			return StopJob{}, false, err
		}
		count, err := changed.RowsAffected()
		if err != nil {
			return StopJob{}, false, err
		}
		if count == 1 {
			if _, err := q.ReleaseReservedPort(ctx, gatewaydb.ReleaseReservedPortParams{ID: uint64(row.PortID.Int64), CurrentOrderID: sql.NullString{String: orderNo, Valid: true}}); err != nil {
				return StopJob{}, false, err
			}
			return StopJob{}, false, tx.Commit()
		}
		return StopJob{}, false, ErrOrderConflict
	}
	if row.Status == gatewaydb.ChargeCommandStatusRejected {
		return StopJob{}, false, tx.Commit()
	}
	if row.Status != gatewaydb.ChargeCommandStatusStopping {
		if err := q.MarkStartStopping(ctx, gatewaydb.MarkStartStoppingParams{CommandID: commandID, Error: sql.NullString{String: "central rejected device start", Valid: true}}); err != nil {
			return StopJob{}, false, err
		}
	}
	job, err := stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.StopSessionID)
	if err != nil {
		return StopJob{}, false, err
	}
	return job, true, tx.Commit()
}

func (s MySQLSink) StoppingJobs(ctx context.Context) ([]StopJob, error) {
	rows, err := gatewaydb.New(s.DB).StoppingCommands(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]StopJob, 0, len(rows))
	for _, row := range rows {
		job, err := stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.StopSessionID)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s MySQLSink) MarkStopSent(ctx context.Context, commandID string) error {
	return gatewaydb.New(s.DB).MarkStopSent(ctx, commandID)
}

func stopJob(commandID, orderNo, deviceID string, port uint8, encoded sql.NullString) (StopJob, error) {
	if !encoded.Valid {
		return StopJob{}, ErrOrderConflict
	}
	value, err := hex.DecodeString(encoded.String)
	if err != nil || len(value) != 6 {
		return StopJob{}, ErrOrderConflict
	}
	var session [6]byte
	copy(session[:], value)
	return StopJob{CommandID: commandID, OrderNo: orderNo, DeviceID: deviceID, PortNo: port,
		Wire: protocol.Command{Kind: protocol.CommandStop, SessionID: session, Port: port}}, nil
}
