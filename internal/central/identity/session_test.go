package identity

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestSessionUserIDJSONPreservesPrecisionAndLegacyRecords(t *testing.T) {
	for _, sample := range []struct {
		raw  string
		want uint64
	}{
		{`42`, 42}, {`"42"`, 42},
		{`923456789012345678`, 923456789012345678}, {`"923456789012345678"`, 923456789012345678},
	} {
		var id sessionUserID
		if err := json.Unmarshal([]byte(sample.raw), &id); err != nil || uint64(id) != sample.want {
			t.Fatalf("read %s lost precision: %d %v", sample.raw, id, err)
		}
		encoded, err := json.Marshal(id)
		if err != nil || string(encoded) != `"`+strconv.FormatUint(sample.want, 10)+`"` {
			t.Fatalf("ID must be stored as an exact string: %s %v", encoded, err)
		}
	}
	for _, raw := range []string{`0`, `"0"`, `null`, `""`, `42.0`, `4.2e1`, `"+42"`, `"-42"`, `" 42 "`, `"18446744073709551616"`} {
		var id sessionUserID
		if err := json.Unmarshal([]byte(raw), &id); err == nil {
			t.Fatalf("accepted invalid session user ID %s", raw)
		}
	}
}

func TestProfileSerializesUserIDAsDecimalString(t *testing.T) {
	const userID = uint64(923456789012345678)
	raw, err := json.Marshal(Profile{UserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || wire.UserID != strconv.FormatUint(userID, 10) {
		t.Fatalf("profile lost ID precision: %s %v", raw, err)
	}
}
