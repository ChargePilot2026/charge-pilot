package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/go-pay/wechat-sdk/mini"
	"github.com/go-pay/xhttp"
	"github.com/redis/go-redis/v9"
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
	cfg, err := config.LoadCentral()
	if err != nil {
		return err
	}
	db, err := dbconn.Open(ctx, cfg.UserDatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	userORM, err := dbconn.WrapGORM(db)
	if err != nil {
		return err
	}
	adminDB, err := dbconn.Open(ctx, cfg.AdminDatabaseURL)
	if err != nil {
		return err
	}
	defer adminDB.Close()
	adminORM, err := dbconn.WrapGORM(adminDB)
	if err != nil {
		return err
	}
	billingDB, err := dbconn.Open(ctx, cfg.BillingDatabaseURL)
	if err != nil {
		return err
	}
	defer billingDB.Close()
	billingORM, err := dbconn.WrapGORM(billingDB)
	if err != nil {
		return err
	}
	redisOptions, err := redis.ParseURL(cfg.RedisCacheURL)
	if err != nil {
		return err
	}
	cache := redis.NewClient(redisOptions)
	defer cache.Close()
	if err := cache.Ping(ctx).Err(); err != nil {
		return err
	}
	jwt, err := auth.NewJWT(cfg.JWTSecret)
	if err != nil {
		return err
	}
	wechat, err := mini.New(cfg.WeChatAppID, cfg.WeChatAppSecret, false)
	if err != nil {
		return err
	}
	wechat.SetHttpClient(xhttp.NewClient().SetTimeout(5 * time.Second))
	gin.SetMode(gin.ReleaseMode)
	// Metrics are exposed unauthenticated so a scraper needs no operator
	// session; the endpoint carries route names, status codes and timings only.
	// The registry is passed into NewRouter so the recording middleware is
	// installed before any route is registered.
	metrics := httpapi.NewMetrics("central")
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		check, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if db.PingContext(check) != nil || adminDB.PingContext(check) != nil || billingDB.PingContext(check) != nil || cache.Ping(check).Err() != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "storage unavailable", nil)
			return
		}
		httpapi.OK(c, gin.H{"status": "identity_storage_ready"})
	})
	adminStore := admin.Store{DB: adminORM}
	if err := adminStore.Bootstrap(ctx, cfg.AdminBootstrapUser, cfg.AdminBootstrapPassword); err != nil {
		return err
	}
	adminAPI := admin.API{Store: adminStore, Sessions: admin.Sessions{Redis: cache}, JWT: jwt}
	adminAPI.Register(router)
	billing.Service{Store: billing.Store{DB: billingORM}, Orders: charge.BillingOrders{DB: userORM}, Splits: billing.SplitResolver{AdminDB: adminORM}, ServiceToken: cfg.ServiceToken,
		Bills: charge.BillIssuer{Store: charge.BillStore{DB: userORM}}}.Register(router)
	admin.ResourceAPI{Store: admin.ResourceStore{AdminDB: adminORM, UserDB: userORM, BillingDB: billingORM}, Auth: adminAPI, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken, ExportDir: os.Getenv("EXPORT_DIR")}.Register(router)
	exportCleanup := admin.ExportTask{Store: admin.ResourceStore{AdminDB: adminORM}, ExportDir: os.Getenv("EXPORT_DIR")}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if count, err := exportCleanup.CleanupExpired(ctx); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("export cleanup: %v", err)
				}
			} else if count > 0 {
				log.Printf("expired %d export files", count)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	admin.Dashboard{UserDB: userORM, AdminDB: adminORM}.Register(router, adminAPI)
	identity.API{WeChat: identity.MiniProgram{SDK: wechat}, Users: identity.UserStore{DB: userORM}, Sessions: identity.Sessions{Redis: cache}, JWT: jwt}.Register(router)
	charge.StartAuthorization{DB: userORM, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.StartResultAPI{Store: charge.StartResultStore{DB: userORM}, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.EndResultAPI{Store: charge.EndResultStore{DB: userORM}, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.StopAuthorization{DB: userORM, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.UserStopAPI{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.ScanAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	var prepay charge.PrepayProvider
	var refundProvider payment.RefundProvider
	switch cfg.PaymentMode {
	case "simulation":
		prepay = payment.Simulator{}
		refundProvider = payment.Simulator{}
		charge.SimulationCallbackAPI{DB: userORM, Store: charge.PaymentCallbackStore{DB: userORM, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}, ServiceToken: cfg.ServiceToken}.Register(router)
	case "wechat_direct":
		direct, err := payment.NewWechatDirect(ctx, payment.Config{AppID: cfg.WeChatAppID, MerchantID: cfg.WechatMchID,
			CertificateSerial: cfg.WechatCertSerial, APIv3Key: cfg.WechatAPIv3Key,
			PrivateKeyPath: cfg.WechatPrivateKey, NotifyURL: cfg.WechatNotifyURL})
		if err != nil {
			return err
		}
		prepay = direct
		refundProvider = direct
		charge.WechatCallbackAPI{Verifier: direct, Store: charge.PaymentCallbackStore{DB: userORM, ExpectedProvider: "wechat_direct", ExpectedMerchantID: cfg.WechatMchID, ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	charge.RefundAPI{Executor: charge.RefundExecutor{DB: userORM, Provider: refundProvider, ProviderName: cfg.PaymentMode}, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.UserAccountAPI{
		Auth:   identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		UserDB: userORM, AdminDB: adminORM, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken,
		Gateway: serviceclient.Client{Timeout: 8 * time.Second}, Prepay: prepay,
		PhoneKey: []byte(cfg.PhoneEncryptionKey),
	}.Register(router)
	charge.BillHTTP{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		Bills: charge.BillStore{DB: userORM}}.Register(router)
	charge.UserQueryAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		DB: userORM, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken, Gateway: serviceclient.Client{Timeout: 8 * time.Second}}.Register(router)
	charge.CouponAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		Coupons: charge.CouponStore{DB: userORM}}.Register(router)
	charge.DebtAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		Debts: charge.DebtStore{DB: userORM}, Provider: prepay}.Register(router)
	charge.PaymentStartAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		Scan: charge.ScanAPI{GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, Pricing: pricing.Store{DB: adminORM},
		Intents: charge.PaymentIntentStore{DB: userORM}, Provider: prepay}.Register(router)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
