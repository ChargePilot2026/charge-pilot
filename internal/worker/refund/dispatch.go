package refund

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/internaljob"
	"net/http"
)

type Dispatcher struct {
	CentralURL, ServiceToken string
	Client                   *http.Client
}

func (d Dispatcher) Run(ctx context.Context) error {
	return internaljob.Run(ctx, d.CentralURL, d.ServiceToken, "/api/v1/internal/refunds/dispatch", d.Client)
}
