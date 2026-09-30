package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"net/http"
)

type CardReplyAPI struct {
	Devices      *protocol.Registry
	ServiceToken string
}

func (a CardReplyAPI) Register(r *gin.Engine) { r.POST("/api/v1/internal/cards/reply", a.reply) }
func (a CardReplyAPI) reply(c *gin.Context) {
	p, e := sha256.Sum256([]byte(c.GetHeader("X-Service-Token"))), sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(p[:], e[:]) != 1 {
		httpapi.Write(c, 401, 1001, "service token invalid", nil)
		return
	}
	var in struct {
		DeviceID     string               `json:"device_id"`
		Session      string               `json:"session"`
		Kind         protocol.CommandKind `json:"kind"`
		CardNumber   uint32               `json:"card_number"`
		BalanceUnits uint16               `json:"balance_units"`
		Invalid      bool                 `json:"invalid"`
	}
	if c.ShouldBindJSON(&in) != nil || in.DeviceID == "" || (in.CardNumber == 0 && !(in.Kind == protocol.CommandCardBalance && in.Invalid)) || (in.Kind != protocol.CommandCardDenied && in.Kind != protocol.CommandCardBalance) {
		httpapi.BadRequest(c, "invalid card reply")
		return
	}
	raw, err := hex.DecodeString(in.Session)
	if err != nil || len(raw) != 6 {
		httpapi.BadRequest(c, "invalid reply session")
		return
	}
	var session [6]byte
	copy(session[:], raw)
	if err := a.Devices.Send(c.Request.Context(), in.DeviceID, protocol.Command{Kind: in.Kind, SessionID: session, CardNumber: in.CardNumber, CardBalanceUnits: in.BalanceUnits, CardInvalid: in.Invalid}); err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "card reply not delivered", nil)
		return
	}
	httpapi.OK(c, gin.H{"accepted": true})
}
