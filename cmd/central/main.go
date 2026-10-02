package main

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/central/coupon"
	"github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/refund"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/card"
	"github.com/ChargePilot2026/charge-pilot/internal/central/channel"
	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
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
	db, err := dbconn.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	userORM, err := dbconn.WrapGORM(db)
	if err != nil {
		return err
	}
	adminDB := db
	adminORM, billingORM := userORM, userORM
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
	// 在注册路由前安装指标中间件，覆盖全部 HTTP 请求。
	// 指标端点无需运营会话，仅暴露路由模板、状态码与耗时。
	metrics := httpapi.NewMetrics("central")
	router := httpapi.NewRouter(metrics)
	metrics.Register(router)
	router.GET("/health/live", func(c *gin.Context) { httpapi.OK(c, gin.H{"status": "live"}) })
	router.GET("/health/ready", func(c *gin.Context) {
		check, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if db.PingContext(check) != nil || cache.Ping(check).Err() != nil {
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
	admin.RegulatoryEventsAPI{Queue: admin.RegulatoryQueue{AdminDB: adminDB}, ServiceToken: cfg.ServiceToken}.Register(router)
	admin.DeliveryDispatchAPI{Webhook: admin.WebhookDispatch{AdminDB: adminORM}, Regulatory: admin.RegulatoryDispatch{AdminDB: adminORM}, ServiceToken: cfg.ServiceToken}.Register(router)
	billing.Service{Store: billing.Store{DB: billingORM}, Orders: settlement.BillingOrders{DB: userORM}, Splits: billing.SplitResolver{AdminDB: adminORM}, ServiceToken: cfg.ServiceToken,
		Bills: settlement.BillIssuer{Store: settlement.BillStore{DB: userORM}}}.Register(router)
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
	var loginExchanger identity.CodeExchanger = identity.MiniProgram{SDK: wechat}
	if cfg.LoginMode == "development" {
		loginExchanger = identity.DevelopmentExchanger{}
	}
	identity.API{WeChat: loginExchanger, Users: identity.UserStore{DB: userORM}, Sessions: identity.Sessions{Redis: cache}, JWT: jwt}.Register(router)
	order.StartAuthorization{DB: userORM, ServiceToken: cfg.ServiceToken}.Register(router)
	order.StartResultAPI{Store: order.StartResultStore{DB: userORM}, ServiceToken: cfg.ServiceToken}.Register(router)
	order.EndResultAPI{Store: order.EndResultStore{DB: userORM}, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.StopAuthorization{DB: userORM, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.UserStopAPI{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	payment.ScanAPI{Operations: payment.DeviceOperationStore{DB: adminORM}, Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}.Register(router)
	var prepay payment.PrepayProvider
	var refundProvider channel.RefundProvider
	switch cfg.PaymentMode {
	case "simulation":
		prepay = channel.Simulator{}
		refundProvider = channel.Simulator{}
		payment.SimulationCallbackAPI{DB: userORM, Store: payment.PaymentCallbackStore{DB: userORM, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}, ServiceToken: cfg.ServiceToken}.Register(router)
		payment.DevelopmentPaymentAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}}, DB: userORM, Store: payment.PaymentCallbackStore{DB: userORM, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	case "wechat_direct":
		direct, err := channel.NewWechatDirect(ctx, channel.Config{AppID: cfg.WeChatAppID, MerchantID: cfg.WechatMchID,
			CertificateSerial: cfg.WechatCertSerial, APIv3Key: cfg.WechatAPIv3Key,
			PrivateKeyPath: cfg.WechatPrivateKey, NotifyURL: cfg.WechatNotifyURL})
		if err != nil {
			return err
		}
		prepay = direct
		refundProvider = direct
		payment.WechatCallbackAPI{Verifier: direct, Store: payment.PaymentCallbackStore{DB: userORM, ExpectedProvider: "wechat_direct", ExpectedMerchantID: cfg.WechatMchID, ExpectedAppID: cfg.WeChatAppID}}.Register(router)
	}
	refund.RefundAPI{Executor: refund.RefundExecutor{DB: userORM, Provider: refundProvider, ProviderName: cfg.PaymentMode}, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.AutoStopAPI{Stopper: charge.AutoStopper{UserDB: userORM, Gateway: serviceclient.Client{Timeout: 10 * time.Second}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, ServiceToken: cfg.ServiceToken}.Register(router)
	admin.DeviceAlertSyncAPI{Sync: admin.DeviceAlertSync{AdminDB: adminORM, Gateway: serviceclient.Client{Timeout: 10 * time.Second}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, ServiceToken: cfg.ServiceToken}.Register(router)
	charge.UserAccountAPI{
		Auth:   identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		UserDB: userORM, AdminDB: adminORM, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken,
		Gateway: serviceclient.Client{Timeout: 8 * time.Second}, Prepay: prepay,
	}.Register(router)
	identity.PhoneAPI{
		Auth:             identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		DB:               userORM,
		DevelopmentPhone: cfg.LoginMode == "development",
		PhoneExchange: func(ctx context.Context, code string) (string, error) {
			result, err := wechat.GetPhoneNumber(ctx, code)
			if err != nil {
				return "", err
			}
			if result == nil || result.Errcode != 0 || result.PhoneInfo == nil {
				return "", errors.New("invalid phone authorization")
			}
			return result.PhoneInfo.PurePhoneNumber, nil
		},
	}.Register(router)
	settlement.BillHTTP{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		Bills: settlement.BillStore{DB: userORM}}.Register(router)
	charge.UserQueryAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		DB: userORM, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken, Gateway: serviceclient.Client{Timeout: 8 * time.Second}}.Register(router)
	coupon.CouponAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		Coupons: coupon.CouponStore{DB: userORM}}.Register(router)
	payment.PaymentStartAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}},
		Scan: payment.ScanAPI{Operations: payment.DeviceOperationStore{DB: adminORM}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}, Pricing: pricing.Store{DB: adminORM},
		Intents: payment.PaymentIntentStore{DB: userORM}, Provider: prepay}.Register(router)
	card.CardAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: userORM}}, Store: card.CardStore{DB: userORM}, Pricing: pricing.Store{DB: adminORM}, Scan: charge.ScanPortLookup{API: payment.ScanAPI{Operations: payment.DeviceOperationStore{DB: adminORM}, GatewayURL: cfg.GatewayInternalURL, ServiceToken: cfg.ServiceToken}}, ServiceToken: cfg.ServiceToken}.Register(router)
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
