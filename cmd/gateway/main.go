package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/control"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
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
	cfg, err := config.LoadGateway()
	if err != nil {
		return err
	}
	db, err := dbconn.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	gin.SetMode(gin.ReleaseMode)
	router := httpapi.NewRouter()
	deviceConnections := &protocol.Registry{}
	control.StartAPI{Service: control.StartService{
		Orders: control.CentralAuthorizer{BaseURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken},
		Store:  store.MySQLSink{DB: db}, Devices: deviceConnections,
	}, ServiceToken: cfg.ServiceToken}.Register(router)
	compensation := control.Compensation{Store: store.MySQLSink{DB: db}, Devices: deviceConnections}
	control.CompensationAPI{Service: compensation, ServiceToken: cfg.ServiceToken}.Register(router)
	userStops := control.UserStopService{Orders: control.CentralAuthorizer{BaseURL: cfg.CentralInternalURL, ServiceToken: cfg.ServiceToken}, Store: store.MySQLSink{DB: db}, Devices: deviceConnections}
	control.UserStopAPI{Service: userStops, ServiceToken: cfg.ServiceToken}.Register(router)
	control.ScanAPI{Store: store.MySQLSink{DB: db}, ServiceToken: cfg.ServiceToken}.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		checkCtx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(checkCtx); err != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "database unavailable", nil)
			return
		}
		httpapi.OK(c, gin.H{"status": "device_ingress_ready"})
	})
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	group.Go(func() error {
		err := protocol.Serve(groupCtx, []protocol.Endpoint{{Address: cfg.DC589Addr, Adapter: dc589.TCPAdapter{Registry: deviceConnections}}}, store.MySQLSink{DB: db}, cfg.MaxConnections)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	})
	group.Go(func() error {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-groupCtx.Done():
				return nil
			case <-ticker.C:
				if err := compensation.RetryStopping(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("STOP compensation: %v", err)
				}
				if err := userStops.RetryPending(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("user STOP retry: %v", err)
				}
			}
		}
	})
	group.Go(func() error {
		<-groupCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})
	if err := group.Wait(); err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	return nil
}
