package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestStationFilterValidation(t *testing.T) {
	for _, tc := range []struct {
		query           string
		required, valid bool
		id              uint64
	}{
		{"", false, true, 0}, {"", true, false, 0}, {"?station_id=17", true, true, 17},
		{"?station_id=", false, false, 0}, {"?station_id=0", false, false, 0},
		{"?station_id=-1", false, false, 0}, {"?station_id=abc", false, false, 0},
		{"?station_id=1&station_id=2", false, false, 0}, {"?station_id=9223372036854775808", false, false, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/"+tc.query, nil)
			id, valid := queryStationID(c, tc.required)
			if valid != tc.valid || id != tc.id {
				t.Fatalf("got (%d,%v), want (%d,%v)", id, valid, tc.id, tc.valid)
			}
			if !valid && w.Code != 400 {
				t.Fatalf("invalid filter returned %d", w.Code)
			}
		})
	}
}

func TestPreserveOverridesQueryValidation(t *testing.T) {
	for _, tc := range []struct {
		query        string
		value, valid bool
	}{
		{"", false, true}, {"?preserve_device_overrides=true", true, true}, {"?preserve_device_overrides=false", false, true},
		{"?preserve_device_overrides=1", false, false}, {"?preserve_device_overrides=", false, false},
		{"?preserve_device_overrides=true&preserve_device_overrides=false", false, false},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/"+tc.query, nil)
		value, valid := queryBool(c, "preserve_device_overrides")
		if value != tc.value || valid != tc.valid {
			t.Fatalf("%s: got (%v,%v)", tc.query, value, valid)
		}
	}
}
