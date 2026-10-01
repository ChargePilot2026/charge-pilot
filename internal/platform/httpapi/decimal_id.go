package httpapi

import (
	"encoding/json"
	"errors"
	"strconv"
)

// DecimalID 接受十进制字符串及兼容旧接口的安全 JSON 整数。
// 大 ID 必须以字符串传输，避免 JavaScript number 在请求发送前丢失精度。
type DecimalID uint64

func (id DecimalID) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(id), 10))
}

func (id *DecimalID) UnmarshalJSON(data []byte) error {
	value := string(data)
	quoted := len(data) > 0 && data[0] == '"'
	if quoted {
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
	}
	if value == "" || len(value) > 20 || len(value) > 1 && value[0] == '0' {
		return errors.New("invalid decimal ID")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return errors.New("invalid decimal ID")
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return err
	}
	if !quoted && parsed > 1<<53-1 {
		return errors.New("large IDs must be sent as decimal strings")
	}
	*id = DecimalID(parsed)
	return nil
}
