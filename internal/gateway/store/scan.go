package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	gatewaydb "github.com/ChargePilot2026/charge-pilot/internal/gateway/store/generated"
)

type ScanPort struct {
	PortID    string `json:"port_id"`
	DeviceID  string `json:"device_id"`
	PortNo    uint8  `json:"port_no"`
	Status    string `json:"port_status"`
	Online    bool   `json:"online"`
	Available bool   `json:"available"`
}

type ScanResult struct {
	Kind     string     `json:"kind"`
	DeviceID string     `json:"device_id"`
	Port     *ScanPort  `json:"port,omitempty"`
	Ports    []ScanPort `json:"ports,omitempty"`
}

var ErrScanNotFound = errors.New("scan code not found")

func (s MySQLSink) ResolveScan(ctx context.Context, code string) (ScanResult, error) {
	q := gatewaydb.New(s.DB)
	port, err := q.ScanPortByCode(ctx, code)
	if err == nil {
		view := scanPort(port.PortCode, port.DeviceID, port.PortNo, port.Status, port.CurrentOrderID, port.LastSeenAt)
		return ScanResult{Kind: "port", DeviceID: view.DeviceID, Port: &view}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ScanResult{}, err
	}
	rows, err := q.ScanPortsByDevice(ctx, code)
	if err != nil {
		return ScanResult{}, err
	}
	if len(rows) == 0 {
		return ScanResult{}, ErrScanNotFound
	}
	result := ScanResult{Kind: "device", DeviceID: code, Ports: make([]ScanPort, 0, len(rows))}
	for _, row := range rows {
		result.Ports = append(result.Ports, scanPort(row.PortCode, row.DeviceID, row.PortNo, row.Status, row.CurrentOrderID, row.LastSeenAt))
	}
	return result, nil
}

func scanPort(code, deviceID string, number uint8, status gatewaydb.DevicePortStatus, current sql.NullString, seen sql.NullTime) ScanPort {
	online := seen.Valid && time.Since(seen.Time) <= 2*time.Minute && time.Since(seen.Time) >= -time.Minute
	return ScanPort{PortID: code, DeviceID: deviceID, PortNo: number, Status: string(status), Online: online,
		Available: status == gatewaydb.DevicePortStatusIdle && !current.Valid && online}
}
