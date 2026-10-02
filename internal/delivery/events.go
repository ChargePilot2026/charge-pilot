package delivery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Event 是与厂商无关的交换对象。
// 面向具体客户的适配器之后可以
// 把它的 Data 翻译成平台自己的字段名。
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

// ValidateEvent 校验事件信封与对象载荷，返回规范化后的 data 字节。
// 校验规则由投递方与入队方共用，保证队列里只有格式一致的事件。
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
