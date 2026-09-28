package charge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type UserStopAPI struct {
	JWT      *auth.JWT
	Sessions interface {
		Exists(context.Context, string) (bool, error)
	}
	Users interface {
		Active(context.Context, uint64) (bool, error)
	}
	GatewayURL   string
	ServiceToken string
	Client       *http.Client
}

func (a UserStopAPI) Register(router *gin.Engine) { router.POST("/api/v1/user/charge/stop", a.handle) }

func (a UserStopAPI) handle(c *gin.Context) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	claims, err := a.JWT.Verify(parts[1], "user", time.Now())
	if err != nil || claims.SessionID == "" {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	valid, err := a.Sessions.Exists(c.Request.Context(), claims.SessionID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "session unavailable", nil)
		return
	}
	if !valid {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "session expired", nil)
		return
	}
	userID, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || userID == 0 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "access token invalid", nil)
		return
	}
	active, err := a.Users.Active(c.Request.Context(), userID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "user unavailable", nil)
		return
	}
	if !active {
		httpapi.Write(c, http.StatusForbidden, 1003, "account frozen", nil)
		return
	}
	var body struct {
		OrderNo string `json:"order_no" binding:"required,max=64"`
	}
	if c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid order number")
		return
	}
	base, err := url.Parse(a.GatewayURL)
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "gateway configuration invalid", nil)
		return
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/stop"
	requestBody, _ := json.Marshal(map[string]any{"order_no": body.OrderNo, "user_id": userID, "source": "user_app"})
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, base.String(), bytes.NewReader(requestBody))
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "gateway request failed", nil)
		return
	}
	request.Header.Set("X-Service-Token", a.ServiceToken)
	request.Header.Set("Content-Type", "application/json")
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "gateway unavailable", nil)
		return
	}
	defer response.Body.Close()
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Accepted  bool   `json:"accepted"`
			Stopped   bool   `json:"stopped"`
			CommandID string `json:"command_id"`
			Status    string `json:"status"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&envelope) != nil {
		httpapi.Write(c, http.StatusBadGateway, 5003, "invalid gateway response", nil)
		return
	}
	if response.StatusCode == http.StatusConflict {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not stoppable", nil)
		return
	}
	if response.StatusCode != http.StatusAccepted || envelope.Code != 0 || !envelope.Data.Accepted {
		httpapi.Write(c, http.StatusBadGateway, 5003, "gateway stop not accepted", nil)
		return
	}
	httpapi.OK(c, gin.H{"accepted": true, "stopped": false, "command_id": envelope.Data.CommandID, "status": envelope.Data.Status})
}
