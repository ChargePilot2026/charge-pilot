package admin

import (
	"context"
	"sync"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

func (a ResourceAPI) orderLive(ctx context.Context, rows []OrderView) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	service := charge.LiveMeterService{DB: a.Store.UserDB, GatewayURL: a.GatewayURL, ServiceToken: a.ServiceToken, Client: serviceclient.Client{Timeout: 2 * time.Second}}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				row := &rows[i]
				view, err := service.Read(ctx, row.OrderID, row.DeviceID, row.PortNo, *row.StartedAt)
				if view != nil {
					row.Live = view
				}
				if err != nil || view == nil {
					row.LiveUnavailable = "设备计量暂不可读取"
				}
			}
		}()
	}
	for i := range rows {
		if rows[i].Status == "charging" && rows[i].StartedAt != nil {
			select {
			case jobs <- i:
			case <-ctx.Done():
				rows[i].LiveUnavailable = "设备计量暂不可读取"
			}
		}
	}
	close(jobs)
	wg.Wait()
}
