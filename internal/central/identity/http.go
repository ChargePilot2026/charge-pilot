package identity

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

const accessTTL = 15 * time.Minute

type UserRepository interface {
	Login(context.Context, string, string) (User, error)
	Active(context.Context, uint64) (bool, error)
	Profile(context.Context, uint64) (Profile, error)
}
type SessionRepository interface {
	Create(context.Context, User) (string, string, error)
	Rotate(context.Context, string) (User, string, string, error)
	Revoke(context.Context, string) error
	Exists(context.Context, string) (bool, error)
}
type API struct {
	WeChat   CodeExchanger
	Users    UserRepository
	Sessions SessionRepository
	JWT      *auth.JWT
}

func (a API) Register(router *gin.Engine) {
	router.POST("/api/v1/public/auth/login", a.login)
	router.POST("/api/v1/public/auth/refresh", a.refresh)
	router.POST("/api/v1/public/auth/logout", a.logout)
	router.GET("/api/v1/user/profile", a.profile)
}

func (a API) login(c *gin.Context) {
	var request struct {
		Code string `json:"code" binding:"required,min=1,max=128"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		httpapi.BadRequest(c, "invalid login code")
		return
	}
	identity, err := a.WeChat.Exchange(c.Request.Context(), request.Code)
	if err != nil {
		httpapi.Write(c, http.StatusBadGateway, 3001, "WeChat login failed", nil)
		return
	}
	user, err := a.Users.Login(c.Request.Context(), identity.OpenID, identity.UnionID)
	if errors.Is(err, ErrUserFrozen) {
		httpapi.Write(c, http.StatusForbidden, 1003, "account frozen", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "login storage unavailable", nil)
		return
	}
	sid, refresh, err := a.Sessions.Create(c.Request.Context(), user)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "session storage unavailable", nil)
		return
	}
	access, err := a.sign(user, sid)
	if err != nil {
		_ = a.Sessions.Revoke(c.Request.Context(), refresh)
		httpapi.Write(c, http.StatusInternalServerError, 5001, "token signing failed", nil)
		return
	}
	httpapi.OK(c, gin.H{"jwt": access, "token": access, "refresh_token": refresh, "user_id": user.ID, "is_new_user": user.IsNew, "jwt_expires_in": int(accessTTL.Seconds())})
}

func (a API) refresh(c *gin.Context) {
	token := bearer(c)
	user, sid, next, err := a.Sessions.Rotate(c.Request.Context(), token)
	if errors.Is(err, ErrInvalidRefresh) {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "refresh token invalid", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "session storage unavailable", nil)
		return
	}
	active, err := a.Users.Active(c.Request.Context(), user.ID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "user storage unavailable", nil)
		return
	}
	if !active {
		_ = a.Sessions.Revoke(c.Request.Context(), next)
		httpapi.Write(c, http.StatusForbidden, 1003, "account frozen", nil)
		return
	}
	access, err := a.sign(user, sid)
	if err != nil {
		httpapi.Write(c, http.StatusInternalServerError, 5001, "token signing failed", nil)
		return
	}
	httpapi.OK(c, gin.H{"jwt": access, "token": access, "refresh_token": next, "jwt_expires_in": int(accessTTL.Seconds())})
}

func (a API) logout(c *gin.Context) {
	if err := a.Sessions.Revoke(c.Request.Context(), bearer(c)); errors.Is(err, ErrInvalidRefresh) {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "refresh token invalid", nil)
		return
	} else if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "session storage unavailable", nil)
		return
	}
	httpapi.OK(c, gin.H{"logged_out": true})
}

func (a API) profile(c *gin.Context) {
	claims, err := a.JWT.Verify(bearer(c), "user", time.Now())
	if err != nil || claims.SessionID == "" {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	valid, err := a.Sessions.Exists(c.Request.Context(), claims.SessionID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "session storage unavailable", nil)
		return
	}
	if !valid {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "session expired", nil)
		return
	}
	id, _ := strconv.ParseUint(claims.Subject, 10, 64)
	active, err := a.Users.Active(c.Request.Context(), id)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "user storage unavailable", nil)
		return
	}
	if !active {
		httpapi.Write(c, http.StatusForbidden, 1003, "account frozen", nil)
		return
	}
	profile, err := a.Users.Profile(c.Request.Context(), id)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "user storage unavailable", nil)
		return
	}
	httpapi.OK(c, profile)
}

func (a API) sign(user User, sid string) (string, error) {
	now := time.Now().Unix()
	return a.JWT.Sign(auth.Claims{Subject: strconv.FormatUint(user.ID, 10), Kind: "user", OpenID: user.OpenID, SessionID: sid, IssuedAt: now, ExpiresAt: now + int64(accessTTL.Seconds())})
}

func bearer(c *gin.Context) string {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
