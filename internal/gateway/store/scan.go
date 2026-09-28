package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
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
	Kind      string     `json:"kind"`
	DeviceID  string     `json:"device_id"`
	StationID uint64     `json:"station_id"`
	Port      *ScanPort  `json:"port,omitempty"`
	Ports     []ScanPort `json:"ports,omitempty"`
}

var ErrScanNotFound = errors.New("scan code not found")

func (s MySQLSink) ResolveScan(ctx context.Context, code string) (ScanResult, error) {
	var port scanPortRow
	err := s.DB.WithContext(ctx).Table("device_port AS p").
		Select("p.id, p.device_id, p.port_no, p.port_code, p.status, p.current_order_id, d.last_seen_at, d.station_id").
		Joins("JOIN device AS d ON d.device_id = p.device_id").Joins("JOIN vendor AS v ON v.id = d.vendor_id").
		Where("p.port_code = ? AND p.deleted_at IS NULL AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.status = 'enabled' AND v.deleted_at IS NULL", code).
		Take(&port).Error
	if err == nil {
		view := scanPort(port.PortCode, port.DeviceID, port.PortNo, port.Status, port.CurrentOrderID, port.LastSeenAt)
		return ScanResult{Kind: "port", DeviceID: view.DeviceID, StationID: uint64(port.StationID.Int64), Port: &view}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ScanResult{}, err
	}
	var rows []scanPortRow
	err = s.DB.WithContext(ctx).Table("device_port AS p").
		Select("p.id, p.device_id, p.port_no, p.port_code, p.status, p.current_order_id, d.last_seen_at, d.station_id").
		Joins("JOIN device AS d ON d.device_id = p.device_id").Joins("JOIN vendor AS v ON v.id = d.vendor_id").
		Where("d.device_id = ? AND p.deleted_at IS NULL AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.status = 'enabled' AND v.deleted_at IS NULL", code).
		Order("p.port_no").Find(&rows).Error
	if err != nil {
		return ScanResult{}, err
	}
	if len(rows) == 0 {
		return ScanResult{}, ErrScanNotFound
	}
	result := ScanResult{Kind: "device", DeviceID: code, StationID: uint64(rows[0].StationID.Int64), Ports: make([]ScanPort, 0, len(rows))}
	for _, row := range rows {
		result.Ports = append(result.Ports, scanPort(row.PortCode, row.DeviceID, row.PortNo, row.Status, row.CurrentOrderID, row.LastSeenAt))
	}
	return result, nil
}

type scanPortRow struct {
	ID             uint64         `gorm:"column:id"`
	DeviceID       string         `gorm:"column:device_id"`
	PortNo         uint8          `gorm:"column:port_no"`
	PortCode       string         `gorm:"column:port_code"`
	Status         string         `gorm:"column:status"`
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
	LastSeenAt     sql.NullTime   `gorm:"column:last_seen_at"`
	StationID      sql.NullInt64  `gorm:"column:station_id"`
}

func scanPort(code, deviceID string, number uint8, status string, current sql.NullString, seen sql.NullTime) ScanPort {
	online := seen.Valid && time.Since(seen.Time) <= 2*time.Minute && time.Since(seen.Time) >= -time.Minute
	return ScanPort{PortID: code, DeviceID: deviceID, PortNo: number, Status: string(status), Online: online,
		Available: status == "idle" && !current.Valid && online}
}
