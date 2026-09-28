package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type StartAPI struct {
	Service      StartService
	ServiceToken string
}

func (a StartAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/charge-orders/start", a.start)
}

func (a StartAPI) start(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	var request struct {
		OrderNo string `json:"order_no" binding:"required,min=1,max=64"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.OrderNo) != request.OrderNo {
		httpapi.BadRequest(c, "invalid order number")
		return
	}
	command, err := a.Service.Start(c.Request.Context(), request.OrderNo)
	if err != nil {
		httpapi.Write(c, startStatusCode(err), 2000, "charge start unavailable", nil)
		return
	}
	httpapi.Write(c, http.StatusAccepted, 0, "accepted", gin.H{
		"order_no":            command.OrderNo,
		"command_id":          command.CommandID,
		"status":              command.Status,
		"device_acknowledged": command.Status == "acked",
	})
}
