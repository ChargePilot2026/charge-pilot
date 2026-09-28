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
	HTTPAddr           string `env:"CENTRAL_HTTP_ADDR" envDefault:":8080"`
	UserDatabaseURL    string `env:"DATABASE_URL_USER,required"`
	AdminDatabaseURL   string `env:"DATABASE_URL_ADMIN,required"`
	BillingDatabaseURL string `env:"DATABASE_URL_BILLING,required"`
	RedisCacheURL      string `env:"REDIS_CACHE_URL,required"`
	RedisStreamURL     string `env:"REDIS_STREAM_URL,required"`
	JWTSecret          string `env:"JWT_SECRET,required"`
	ServiceToken       string `env:"SERVICE_TOKEN,required"`
	GatewayInternalURL string `env:"GATEWAY_INTERNAL_URL" envDefault:"http://gateway:8083"`
	WeChatAppID        string `env:"WECHAT_APPID,required"`
	WeChatAppSecret    string `env:"WECHAT_SECRET,required"`
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
	return c, nil
}

func LoadWorker() (Worker, error) {
	var c Worker
	if err := env.Parse(&c); err != nil {
		return c, fmt.Errorf("worker config: %w", err)
	}
	return c, nil
}
