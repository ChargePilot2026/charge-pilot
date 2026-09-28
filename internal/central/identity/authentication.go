package identity

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type SessionAuthenticator struct {
	JWT      *auth.JWT
	Sessions interface {
		Exists(context.Context, string) (bool, error)
	}
	Users interface {
		Active(context.Context, uint64) (bool, error)
	}
}

// Authenticate verifies the signed token, live session, and current account
// state on every user request. The caller must return when ok is false.
func (a SessionAuthenticator) Authenticate(c *gin.Context) (userID uint64, ok bool) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return 0, false
	}
	claims, err := a.JWT.Verify(parts[1], "user", time.Now())
	if err != nil || claims.SessionID == "" {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return 0, false
	}
	valid, err := a.Sessions.Exists(c.Request.Context(), claims.SessionID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "session unavailable", nil)
		return 0, false
	}
	if !valid {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "session expired", nil)
		return 0, false
	}
	id, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || id == 0 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return 0, false
	}
	active, err := a.Users.Active(c.Request.Context(), id)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "user unavailable", nil)
		return 0, false
	}
	if !active {
		httpapi.Write(c, http.StatusForbidden, 1003, "account frozen", nil)
		return 0, false
	}
	return id, true
}
