package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/alerts"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/internaljob"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/outbox"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/refund"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/schedule"
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
	databaseURLs := map[string]string{"gateway": cfg.GatewayDatabaseURL, "central": cfg.CentralDatabaseURL, "worker": cfg.DatabaseURL}
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
	databases["user"] = databases["central"]
	orms["user"] = orms["central"]
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
	// 在注册路由前安装指标中间件，覆盖全部 HTTP 请求。
	// 指标端点无需运营会话，仅暴露路由模板、状态码与耗时。
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
		for name := range databaseURLs {
			if databases[name].PingContext(check) != nil {
				httpapi.Write(c, http.StatusServiceUnavailable, 5003, "database unavailable", nil)
				return
			}
		}
		httpapi.OK(c, gin.H{"status": "outbox_ready"})
	})
	publishers := []outbox.Publisher{
		{Source: "gateway", DB: orms["gateway"], Stream: stream},
		{Source: "user", DB: orms["user"], Stream: stream},
		{Source: "admin", DB: orms["central"], Stream: stream},
	}
	// 内部服务调用共享连接池：此前各组件 Client 字段均未赋值，
	// 每次请求新建 &http.Client{}，连接池完全不生效。
	// 超时沿用各组件原回退值：内部同步调用 5s，长任务派发 30s，webhook 外投 10s。
	internalHTTP := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	jobHTTP := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	webhookHTTP := &http.Client{Timeout: 10 * time.Second}
	startResults := charge.Synchronizer{Gateway: serviceclient.Client{HTTP: internalHTTP}, GatewayURL: cfg.GatewayInternalURL, CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: internalHTTP}
	endResults := charge.EndSynchronizer{Gateway: serviceclient.Client{HTTP: internalHTTP}, GatewayURL: cfg.GatewayInternalURL, CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: internalHTTP}
	paidStarts := charge.PaidStarter{UserDB: orms["user"], GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken, Client: internalHTTP}
	cardEvents := charge.CardDispatcher{Gateway: serviceclient.Client{HTTP: internalHTTP}, CentralURL: cfg.CentralInternalURL, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken, Client: internalHTTP}
	autoStops := charge.AutoStopper{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: serviceclient.Client{HTTP: jobHTTP}}
	billingJobs := billing.Dispatcher{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: jobHTTP}
	refunds := refund.Dispatcher{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: jobHTTP}
	// webhook/regulatory 只向进程外投递：订阅清单、投递日志与报送状态
	// 都经 central 内部端点读写，worker 不再持有 central 的 ORM/DB 句柄。
	webhooks := delivery.WebhookDeliverer{Stream: stream, Central: serviceclient.Client{HTTP: webhookHTTP}, CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: webhookHTTP}
	var regulatoryReports *delivery.RegulatoryDeliverer
	switch cfg.RegulatoryMode {
	case "simulation":
		regulatoryReports = &delivery.RegulatoryDeliverer{Central: serviceclient.Client{HTTP: internalHTTP}, CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Sender: delivery.SimulationSender{}}
	case "http":
		regulatoryReports = &delivery.RegulatoryDeliverer{Central: serviceclient.Client{HTTP: internalHTTP}, CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Sender: delivery.HTTPSender{Endpoint: cfg.RegulatoryEndpoint, Secret: cfg.RegulatorySecret, Client: webhookHTTP}}
	}
	dlq := outbox.DLQ{WorkerDB: orms["worker"], Stream: stream, Consumer: "worker-dlq", Streams: outbox.DefaultBusinessStreams}
	// 死信重放按流分发：当前各业务流均无注册的重放处理器，
	// 显式报错而不是假装成功；后续批次接入新处理器时在此登记。
	replay := func(ctx context.Context, stream, eventID, source string, payload []byte) error {
		return fmt.Errorf("stream %s has no registered replay handler", stream)
	}
	internaljob.OpsAPI{WorkerDB: orms["worker"], ServiceToken: cfg.ServiceToken, DLQ: dlq, Replay: replay}.Register(router)
	deviceAlerts := alerts.Sync{CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken, Client: serviceclient.Client{HTTP: internalHTTP}}
	scheduler := schedule.Scheduler{DB: orms["worker"], Handlers: map[string]schedule.Handler{
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
		nextCleanup := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Now().After(nextCleanup) {
					if err := scheduler.CleanupHistory(ctx); err != nil && !errors.Is(err, context.Canceled) {
						log.Printf("scheduled history cleanup: %v", err)
					}
					nextCleanup = time.Now().Add(time.Hour)
				}
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
				// 退款结算由 central 的 RefundExecutor 在派发行内直接完成，
				// worker 不再消费退款结果流（原 ResultConsumer 已删除）。
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
