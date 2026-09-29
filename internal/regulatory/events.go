package regulatory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Event is the vendor-independent interchange object. A customer-specific
// adapter may translate its Data into the platform's own field names later.
type Event struct {
	EventID    string          `json:"event_id" validate:"required,uuid"`
	ObjectType string          `json:"object_type" validate:"required,oneof=operator station device order alert battery"`
	ObjectKey  string          `json:"object_key" validate:"required,max=128"`
	Data       json.RawMessage `json:"data" validate:"required"`
}

type operatorData struct {
	VendorID string `json:"vendor_id" validate:"required"`
	Name     string `json:"name" validate:"required"`
	Contact  string `json:"contact" validate:"required"`
}
type stationData struct {
	StationID   string   `json:"station_id" validate:"required"`
	Name        string   `json:"name" validate:"required"`
	Address     string   `json:"address" validate:"required"`
	Longitude   *float64 `json:"longitude" validate:"required,gte=-180,lte=180"`
	Latitude    *float64 `json:"latitude" validate:"required,gte=-90,lte=90"`
	ServiceType string   `json:"service_type" validate:"required"`
}
type deviceData struct {
	DeviceID        string `json:"device_id" validate:"required"`
	Model           string `json:"model" validate:"required"`
	Protocol        string `json:"protocol" validate:"required"`
	FirmwareVersion string `json:"firmware_version" validate:"required"`
}
type orderData struct {
	OrderID     string `json:"order_id" validate:"required"`
	StartedAt   string `json:"started_at" validate:"required,datetime=2006-01-02T15:04:05Z07:00"`
	EndedAt     string `json:"ended_at" validate:"required,datetime=2006-01-02T15:04:05Z07:00"`
	EnergyKWh   string `json:"energy_kwh" validate:"required"`
	AmountCents *int64 `json:"amount_cents" validate:"required,gte=0"`
}
type alertData struct {
	AlertID  string `json:"alert_id" validate:"required"`
	Type     string `json:"type" validate:"required"`
	Severity string `json:"severity" validate:"required,oneof=warning critical fatal"`
	Time     string `json:"time" validate:"required,datetime=2006-01-02T15:04:05Z07:00"`
}
type batteryData struct {
	BatteryCode string   `json:"battery_code" validate:"required"`
	SOC         *float64 `json:"soc" validate:"required,gte=0,lte=100"`
	Health      *float64 `json:"health,omitempty" validate:"omitempty,gte=0,lte=100"`
}

var validate = validator.New()

func ValidateEvent(event Event) ([]byte, error) {
	if err := validate.Struct(event); err != nil {
		return nil, fmt.Errorf("invalid event envelope: %w", err)
	}
	if uuid.Validate(event.EventID) != nil || strings.TrimSpace(event.ObjectKey) == "" || len(event.Data) > 64*1024 {
		return nil, errors.New("invalid event id, key or payload size")
	}
	var object any
	switch event.ObjectType {
	case "operator":
		object = &operatorData{}
	case "station":
		object = &stationData{}
	case "device":
		object = &deviceData{}
	case "order":
		object = &orderData{}
	case "alert":
		object = &alertData{}
	case "battery":
		object = &batteryData{}
	}
	if err := json.Unmarshal(event.Data, object); err != nil {
		return nil, err
	}
	if err := validate.Struct(object); err != nil {
		return nil, fmt.Errorf("invalid %s object: %w", event.ObjectType, err)
	}
	var identity string
	switch data := object.(type) {
	case *operatorData:
		identity = data.VendorID
	case *stationData:
		identity = data.StationID
	case *deviceData:
		identity = data.DeviceID
	case *orderData:
		identity = data.OrderID
	case *alertData:
		identity = data.AlertID
	case *batteryData:
		identity = data.BatteryCode
	}
	if identity != event.ObjectKey {
		return nil, errors.New("object_key does not match data identity")
	}
	if order, ok := object.(*orderData); ok {
		started, startErr := time.Parse(time.RFC3339, order.StartedAt)
		ended, endErr := time.Parse(time.RFC3339, order.EndedAt)
		energy, energyErr := decimal.NewFromString(order.EnergyKWh)
		if startErr != nil || endErr != nil || !started.Before(ended) || energyErr != nil || energy.IsNegative() {
			return nil, errors.New("invalid order time window or energy")
		}
	}
	var normalized any
	if err := json.Unmarshal(event.Data, &normalized); err != nil {
		return nil, err
	}
	if _, okay := normalized.(map[string]any); !okay {
		return nil, errors.New("event data must be an object")
	}
	return json.Marshal(normalized)
}

type Queue struct{ DB *sql.DB }

var ErrEventConflict = errors.New("event_id already exists with different content")

// Enqueue is idempotent by event_id. An event ID cannot be reused with a
// changed type, key or data, so a retry cannot silently rewrite an audit fact.
func (q Queue) Enqueue(ctx context.Context, event Event) (bool, error) {
	data, err := ValidateEvent(event)
	if err != nil {
		return false, err
	}
	_, err = q.DB.ExecContext(ctx, `INSERT INTO regulatory_report(event_id,object_type,object_key,payload_json) VALUES(?,?,?,?)`,
		event.EventID, event.ObjectType, event.ObjectKey, string(data))
	if err == nil {
		return true, nil
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return false, err
	}
	var existingType, existingKey, existingData string
	if err := q.DB.QueryRowContext(ctx, `SELECT object_type,object_key,CAST(payload_json AS CHAR) FROM regulatory_report WHERE event_id = ?`, event.EventID).
		Scan(&existingType, &existingKey, &existingData); err != nil {
		return false, err
	}
	var normalized any
	if err := json.Unmarshal([]byte(existingData), &normalized); err != nil {
		return false, err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return false, err
	}
	if existingType != event.ObjectType || existingKey != event.ObjectKey || !bytes.Equal(canonical, data) {
		return false, ErrEventConflict
	}
	return false, nil
}
