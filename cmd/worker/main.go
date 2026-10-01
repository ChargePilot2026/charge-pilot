package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/regulatory"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/alerts"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/internaljob"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/outbox"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/refund"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/schedule"
	webhookdelivery "github.com/ChargePilot2026/charge-pilot/internal/worker/webhook"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	databaseURLs := map[string]string{"gateway": cfg.GatewayDatabaseURL, "user": cfg.UserDatabaseURL, "admin": cfg.AdminDatabaseURL, "worker": cfg.DatabaseURL}
	databases := make(map[string]*sql.DB, len(databaseURLs))
	orms := make(map[string]*gorm.DB, len(databaseURLs))
	for name, url := range databaseURLs {
		db, err := dbconn.Open(ctx, url)
		if err != nil {
			return err
		}
		defer db.Close()
		databases[name] = db
		orm, err := dbconn.WrapGORM(db)
		if err != nil {
			return err
		}
		orms[name] = orm
	}
	streamOptions, err := redis.ParseURL(cfg.RedisStreamURL)
	if err != nil {
		return err
	}
	stream := redis.NewClient(streamOptions)
	defer stream.Close()
	if err := stream.Ping(ctx).Err(); err != nil {
		return err
	}
	gin.SetMode(gin.ReleaseMode)
	// 指标端点不做鉴权，抓取器因此不必持有运营会话；它只暴露路由名、状态码和耗时。
	// registry 是在 NewRouter 之前传进去的，这样记录用的中间件会在任何路由注册之前装好。
	metrics := httpapi.NewMetrics("worker")
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		check, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if stream.Ping(check).Err() != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "stream unavailable", nil)
			return
		}
		for _, db := range databases {
			if db.PingContext(check) != nil {
				httpapi.Write(c, http.StatusServiceUnavailable, 5003, "database unavailable", nil)
				return
			}
		}
		httpapi.OK(c, gin.H{"status": "outbox_ready"})
	})
	publishers := []outbox.Publisher{
		{Source: "gateway", DB: orms["gateway"], Stream: stream},
		{Source: "user", DB: orms["user"], Stream: stream},
		{Source: "admin", DB: orms["admin"], Stream: stream},
	}
	startResults := charge.Synchronizer{GatewayDB: orms["gateway"], CentralURL: cfg.CentralInternalURL, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}
	endResults := charge.EndSynchronizer{GatewayDB: orms["gateway"], CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken}
	paidStarts := charge.PaidStarter{UserDB: orms["user"], GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}
	cardEvents := charge.CardDispatcher{GatewayDB: orms["gateway"], CentralURL: cfg.CentralInternalURL, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}
	autoStops := charge.AutoStopper{UserDB: orms["user"], GatewayDB: orms["gateway"], GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}
	billingJobs := billing.Dispatcher{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken}
	refunds := refund.Dispatcher{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken}
	webhooks := webhookdelivery.WebhookDeliverer{AdminDB: orms["admin"], Stream: stream}
	var regulatoryReports *regulatory.Deliverer
	switch cfg.RegulatoryMode {
	case "simulation":
		regulatoryReports = &regulatory.Deliverer{DB: databases["admin"], Sender: regulatory.SimulationSender{}}
	case "http":
		regulatoryReports = &regulatory.Deliverer{DB: databases["admin"], Sender: regulatory.HTTPSender{Endpoint: cfg.RegulatoryEndpoint, Secret: cfg.RegulatorySecret}}
	}
	dlq := outbox.DLQ{WorkerDB: orms["worker"], Stream: stream, Consumer: "worker-dlq"}
	internaljob.OpsAPI{WorkerDB: orms["worker"], ServiceToken: cfg.ServiceToken, DLQ: dlq}.Register(router)
	// 退款结果是从 channel 异步送来的；消费端保证只投递一次，并把每次尝试记进 comp_tx_log。
	refundResults := outbox.ResultConsumer{UserDB: orms["user"], WorkerDB: orms["worker"], Stream: stream, Group: "refund-result"}
	alertEngine := alerts.Evaluator{GatewayDB: orms["gateway"], AdminDB: orms["admin"]}
	deviceAlerts := alerts.DeviceSynchronizer{GatewayDB: orms["gateway"], AdminDB: orms["admin"]}
	scheduler := schedule.Scheduler{DB: orms["worker"], Handlers: map[string]schedule.Handler{
		"alert_evaluate": func(ctx context.Context) (uint64, error) {
			count, err := alertEngine.Evaluate(ctx)
			return uint64(count), err
		},
		"webhook_dispatch": func(ctx context.Context) (uint64, error) {
			count, err := webhooks.PublishBatch(ctx)
			return uint64(count), err
		},
	}}
	schedule.API{Scheduler: scheduler, ServiceToken: cfg.ServiceToken}.Register(router)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := scheduler.RunDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("scheduled tasks: %v", err)
				}
			}
		}
	}()
	refundCtx, stopRefunds := context.WithCancel(ctx)
	defer stopRefunds()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-refundCtx.Done():
				return
			case <-ticker.C:
				if _, err := autoStops.Run(refundCtx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("charge auto stop: %v", err)
				}
				if err := billingJobs.Run(refundCtx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("billing dispatch: %v", err)
				}
				if err := refunds.Run(refundCtx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("refund dispatch: %v", err)
				}
				if _, err := refundResults.ConsumeOnce(refundCtx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("refund result consumer: %v", err)
				}
				if regulatoryReports != nil {
					if _, err := regulatoryReports.RunBatch(refundCtx); err != nil && !errors.Is(err, context.Canceled) {
						log.Printf("regulatory delivery: %v", err)
					}
				}
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-serverErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			if _, err := deviceAlerts.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("device alerts: %v", err)
			}
			if _, err := cardEvents.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("card events: %v", err)
			}
			if _, err := paidStarts.DispatchBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("paid charge starts: %v", err)
			}
			if _, err := startResults.SyncBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("charge start results: %v", err)
			}
			if _, err := endResults.SyncBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("charge end results: %v", err)
			}
			for _, publisher := range publishers {
				if _, err := publisher.PublishBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("outbox %s: %v", publisher.Source, err)
				}
			}
		}
	}
}
