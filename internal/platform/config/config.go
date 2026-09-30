package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

type Gateway struct {
	HTTPAddr           string `env:"GATEWAY_HTTP_ADDR" envDefault:":8083"`
	DC589Addr          string `env:"DC589_ADDR" envDefault:":9100"`
	DatabaseURL        string `env:"DATABASE_URL,required"`
	ServiceToken       string `env:"SERVICE_TOKEN,required"`
	CentralInternalURL string `env:"CENTRAL_INTERNAL_URL" envDefault:"http://central:8080"`
	MaxConnections     int    `env:"DEVICE_MAX_CONNECTIONS" envDefault:"10000"`
}

type Central struct {
	AdminBootstrapUser     string `env:"ADMIN_BOOTSTRAP_USER"`
	AdminBootstrapPassword string `env:"ADMIN_BOOTSTRAP_PASSWORD"`
	HTTPAddr               string `env:"CENTRAL_HTTP_ADDR" envDefault:":8080"`
	UserDatabaseURL        string `env:"DATABASE_URL_USER,required"`
	AdminDatabaseURL       string `env:"DATABASE_URL_ADMIN,required"`
	BillingDatabaseURL     string `env:"DATABASE_URL_BILLING,required"`
	RedisCacheURL          string `env:"REDIS_CACHE_URL,required"`
	RedisStreamURL         string `env:"REDIS_STREAM_URL,required"`
	JWTSecret              string `env:"JWT_SECRET,required"`
	ServiceToken           string `env:"SERVICE_TOKEN,required"`
	GatewayInternalURL     string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	WeChatAppID            string `env:"WECHAT_APPID,required"`
	WeChatAppSecret        string `env:"WECHAT_SECRET,required"`
	PaymentMode            string `env:"PAYMENT_MODE" envDefault:"disabled"`
	WechatMchID            string `env:"WECHAT_MCH_ID"`
	WechatCertSerial       string `env:"WECHAT_CERT_SERIAL"`
	WechatAPIv3Key         string `env:"WECHAT_APIV3_KEY"`
	WechatPrivateKey       string `env:"WECHAT_PRIVATE_KEY_PATH"`
	WechatNotifyURL        string `env:"WECHAT_NOTIFY_URL"`
	// PhoneEncryptionKey 保护存储的手机号。它必须是 32 字节（AES-256），
	// 并且要跨重启保持稳定；没有它就直接拒绝手机号绑定，
	// 而不是退回去用明文存。
	PhoneEncryptionKey string `env:"PHONE_ENCRYPTION_KEY" envDefault:"local-dev-phone-key-32-bytes-xxx"`
}

type Worker struct {
	HTTPAddr           string `env:"WORKER_HTTP_ADDR" envDefault:":8085"`
	DatabaseURL        string `env:"DATABASE_URL,required"`
	GatewayDatabaseURL string `env:"DATABASE_URL_GATEWAY,required"`
	UserDatabaseURL    string `env:"DATABASE_URL_USER,required"`
	AdminDatabaseURL   string `env:"DATABASE_URL_ADMIN,required"`
	RedisStreamURL     string `env:"REDIS_STREAM_URL,required"`
	ServiceToken       string `env:"SERVICE_TOKEN,required"`
	CentralInternalURL string `env:"CENTRAL_INTERNAL_URL" envDefault:"http://central:8080"`
	GatewayInternalURL string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	RegulatoryMode     string `env:"REGULATORY_MODE" envDefault:"disabled"`
	RegulatoryEndpoint string `env:"REGULATORY_ENDPOINT"`
	RegulatorySecret   string `env:"REGULATORY_SIGNING_SECRET"`
}

func LoadGateway() (Gateway, error) {
	var c Gateway
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("gateway config: %w", err)
	}
	if c.MaxConnections < 1 {
		return c, fmt.Errorf("gateway config: DEVICE_MAX_CONNECTIONS must be positive")
	}
	return c, nil
}

func LoadCentral() (Central, error) {
	var c Central
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("central config: %w", err)
	}
	if len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("central config: JWT_SECRET must have at least 32 bytes")
	}
	if c.PaymentMode != "disabled" && c.PaymentMode != "simulation" && c.PaymentMode != "wechat_direct" {
		return c, fmt.Errorf("central config: invalid PAYMENT_MODE")
	}
	if c.PaymentMode == "wechat_direct" && (c.WechatMchID == "" || c.WechatCertSerial == "" || len(c.WechatAPIv3Key) != 32 || c.WechatPrivateKey == "" || c.WechatNotifyURL == "") {
		return c, fmt.Errorf("central config: incomplete WeChat Pay credentials")
	}
	return c, nil
}

func LoadWorker() (Worker, error) {
	var c Worker
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("worker config: %w", err)
	}
	if c.RegulatoryMode != "disabled" && c.RegulatoryMode != "simulation" && c.RegulatoryMode != "http" {
		return c, fmt.Errorf("worker config: invalid REGULATORY_MODE")
	}
	if c.RegulatoryMode == "http" && (c.RegulatoryEndpoint == "" || len(c.RegulatorySecret) < 16) {
		return c, fmt.Errorf("worker config: REGULATORY_ENDPOINT and REGULATORY_SIGNING_SECRET required for http mode")
	}
	return c, nil
}
