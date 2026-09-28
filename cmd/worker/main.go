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
	"github.com/ChargePilot2026/charge-pilot/internal/worker/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/outbox"
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
	router := httpapi.NewRouter()
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
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	publishers := []outbox.Publisher{
		{Source: "gateway", DB: orms["gateway"], Stream: stream},
		{Source: "user", DB: orms["user"], Stream: stream},
		{Source: "admin", DB: orms["admin"], Stream: stream},
	}
	startResults := charge.Synchronizer{GatewayDB: orms["gateway"], CentralURL: cfg.CentralInternalURL, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}
	endResults := charge.EndSynchronizer{GatewayDB: orms["gateway"], CentralURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken}
	paidStarts := charge.PaidStarter{UserDB: orms["user"], GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}
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
