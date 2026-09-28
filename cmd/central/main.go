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

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/config"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
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
	adminDB, err := dbconn.Open(ctx, cfg.AdminDatabaseURL)
	if err != nil {
		return err
	}
	defer adminDB.Close()
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
	router := httpapi.NewRouter()
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		check, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if db.PingContext(check) != nil || adminDB.PingContext(check) != nil || cache.Ping(check).Err() != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "storage unavailable", nil)
			return
		}
		httpapi.OK(c, gin.H{"status": "identity_storage_ready"})
	})
	identity.API{WeChat: identity.MiniProgram{SDK: wechat}, Users: identity.UserStore{DB: db}, Sessions: identity.Sessions{Redis: cache}, JWT: jwt}.Register(router)
	charge.StartAuthorization{DB: db, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.StartResultAPI{Store: charge.StartResultStore{DB: db}, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.EndResultAPI{Store: charge.EndResultStore{DB: db}, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.StopAuthorization{DB: db, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.UserStopAPI{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: db}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.ScanAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: db}}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	var prepay charge.PrepayProvider
	switch cfg.PaymentMode {
	case "simulation":
		prepay = payment.Simulator{}
		charge.SimulationCallbackAPI{DB: db, Store: charge.PaymentCallbackStore{DB: db, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}, ServiceToken: cfg.ServiceToken}.Register(router)
	case "wechat_direct":
		direct, err := payment.NewWechatDirect(ctx, payment.Config{AppID: cfg.WeChatAppID, MerchantID: cfg.WechatMchID,
			CertificateSerial: cfg.WechatCertSerial, APIv3Key: cfg.WechatAPIv3Key,
			PrivateKeyPath: cfg.WechatPrivateKey, NotifyURL: cfg.WechatNotifyURL})
		if err != nil {
			return err
		}
		prepay = direct
		charge.WechatCallbackAPI{Verifier: direct, Store: charge.PaymentCallbackStore{DB: db, ExpectedProvider: "wechat_direct", ExpectedMerchantID: cfg.WechatMchID, ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	charge.PaymentStartAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: db}},
		Scan: charge.ScanAPI{GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, Pricing: pricing.Store{DB: adminDB},
		Intents: charge.PaymentIntentStore{DB: db}, Provider: prepay}.Register(router)
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
