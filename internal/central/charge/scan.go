package charge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

var userScanCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

type ScanPort struct {
	PortID    string `json:"port_id"`
	DeviceID  string `json:"device_id"`
	PortNo    uint8  `json:"port_no"`
	Status    string `json:"port_status"`
	Online    bool   `json:"online"`
	Available bool   `json:"available"`
}

type ScanResult struct {
	Kind      string     `json:"kind"`
	DeviceID  string     `json:"device_id"`
	StationID uint64     `json:"station_id"`
	Port      *ScanPort  `json:"port,omitempty"`
	Ports     []ScanPort `json:"ports,omitempty"`
}

type ScanAPI struct {
	Auth         identity.SessionAuthenticator
	GatewayURL   string
	ServiceToken string
	Client       *http.Client
}

func (a ScanAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/user/scan/resolve", a.resolve)
	router.POST("/api/v1/user/scan/port", a.port)
}

func (a ScanAPI) resolve(c *gin.Context) { a.handle(c, false) }
func (a ScanAPI) port(c *gin.Context)    { a.handle(c, true) }

func (a ScanAPI) handle(c *gin.Context, requirePort bool) {
	if _, ok := a.Auth.Authenticate(c); !ok {
		return
	}
	var body struct {
		Code   string `json:"code"`
		PortID string `json:"port_id"`
	}
	if c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid scan request")
		return
	}
	code := body.Code
	if requirePort {
		code = body.PortID
	}
	if !userScanCodePattern.MatchString(code) {
		httpapi.BadRequest(c, "invalid scan code")
		return
	}
	result, status := a.lookup(c.Request.Context(), code)
	if status != http.StatusOK {
		switch status {
		case http.StatusNotFound:
			httpapi.Write(c, http.StatusNotFound, 1004, "scan code not found", nil)
		default:
			httpapi.Write(c, http.StatusServiceUnavailable, 5001, "device lookup unavailable", nil)
		}
		return
	}
	if requirePort {
		if result.Kind != "port" || result.Port == nil {
			httpapi.Write(c, http.StatusNotFound, 1004, "port not found", nil)
			return
		}
		httpapi.OK(c, result.Port)
		return
	}
	httpapi.OK(c, result)
}

func (a ScanAPI) lookup(ctx context.Context, code string) (ScanResult, int) {
	base, err := url.Parse(a.GatewayURL)
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" || a.ServiceToken == "" {
		return ScanResult{}, http.StatusServiceUnavailable
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/scan/resolve"
	base.RawQuery = url.Values{"code": {code}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return ScanResult{}, http.StatusServiceUnavailable
	}
	request.Header.Set("X-Service-Token", a.ServiceToken)
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return ScanResult{}, http.StatusServiceUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ScanResult{}, http.StatusNotFound
	}
	if response.StatusCode != http.StatusOK {
		return ScanResult{}, http.StatusServiceUnavailable
	}
	var envelope struct {
		Code int        `json:"code"`
		Data ScanResult `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&envelope) != nil || envelope.Code != 0 || envelope.Data.DeviceID == "" {
		return ScanResult{}, http.StatusServiceUnavailable
	}
	return envelope.Data, http.StatusOK
}
