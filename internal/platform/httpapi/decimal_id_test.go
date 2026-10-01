package httpapi

import (
	"encoding/json"
	"testing"
)

func TestDecimalIDPreservesLargeIDsAndAcceptsLegacySmallIntegers(t *testing.T) {
	for _, raw := range []string{`"893327963750400123"`, `"7"`, `7`} {
		var id DecimalID
		if err := json.Unmarshal([]byte(raw), &id); err != nil {
			t.Fatal(raw, err)
		}
		encoded, err := json.Marshal(id)
		if err != nil || string(encoded) != `"`+strconvID(raw)+`"` {
			t.Fatal(raw, string(encoded), err)
		}
	}
}

func strconvID(raw string) string {
	if raw[0] == '"' {
		return raw[1 : len(raw)-1]
	}
	return raw
}

func TestDecimalIDRejectsLossyAndInvalidNumbers(t *testing.T) {
	for _, raw := range []string{`893327963750400123`, `1.5`, `1e3`, `null`, `-1`, `""`, `"01"`, `"1.5"`, `"18446744073709551616"`} {
		var id DecimalID
		if err := json.Unmarshal([]byte(raw), &id); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
