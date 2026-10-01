package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

func TestIssuedAdminAccessTokenLastsEightHours(t *testing.T) {
	jwt, err := auth.NewJWT(strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	router.GET("/issue", func(c *gin.Context) {
		(API{JWT: jwt}).issue(c, Profile{ID: 7, RoleID: 8}, "fixture-session", "fixture-refresh")
	})
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, httptest.NewRequest(http.MethodGet, "/issue", nil))
	if reply.Code != http.StatusOK {
		t.Fatalf("issue status = %d", reply.Code)
	}
	var envelope struct {
		Data struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Token == "" || envelope.Data.AccessToken != envelope.Data.Token {
		t.Fatal("access token aliases differ or are empty")
	}
	claims, err := jwt.Verify(envelope.Data.AccessToken, "admin", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const lifetimeSeconds = int64(8 * time.Hour / time.Second)
	if lifetime := claims.ExpiresAt - claims.IssuedAt; lifetime != lifetimeSeconds || envelope.Data.ExpiresIn != lifetime {
		t.Fatalf("JWT lifetime = %d, expires_in = %d, want %d", lifetime, envelope.Data.ExpiresIn, lifetimeSeconds)
	}
	if claims.Subject != "7" || claims.SessionID != "fixture-session" {
		t.Fatal("issued token lost account or session identity")
	}
	for _, tc := range []struct {
		name  string
		at    int64
		valid bool
	}{
		{"after_previous_expiry", claims.IssuedAt + 16*60, true},
		{"before_eight_hours", claims.ExpiresAt - 1, true},
		{"at_eight_hours", claims.ExpiresAt, false},
		{"after_eight_hours", claims.ExpiresAt + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jwt.Verify(envelope.Data.AccessToken, "admin", time.Unix(tc.at, 0))
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("valid = %t, verification error = %v", tc.valid, err)
			}
		})
	}
}
