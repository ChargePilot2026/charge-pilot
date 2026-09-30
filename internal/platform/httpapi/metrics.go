package httpapi

import (
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 每个服务各持有一个 registry，并使用匹配到的 Gin 路由模板，
// 这样即使 URL 里含有设备 ID，时间序列的基数也是有界的。
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	handler  http.Handler
}

func NewMetrics(service string) *Metrics {
	registry := prometheus.NewRegistry()
	started := time.Now()
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "chargepilot_http_requests_total", Help: "Handled HTTP requests.",
	}, []string{"method", "route", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "chargepilot_http_request_duration_seconds", Help: "HTTP request latency in seconds.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"method", "route"})
	registry.MustRegister(requests, duration,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "chargepilot_service_info", Help: "Always 1; the service this scrape came from.",
			ConstLabels: prometheus.Labels{"service": service},
		}, func() float64 { return 1 }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "chargepilot_build_info", Help: "Build and start information.",
			ConstLabels: prometheus.Labels{"service": service, "go_version": runtime.Version()},
		}, func() float64 { return 1 }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "chargepilot_uptime_seconds", Help: "Seconds since this process started.",
			ConstLabels: prometheus.Labels{"service": service},
		}, func() float64 { return time.Since(started).Seconds() }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "chargepilot_goroutines", Help: "Number of goroutines.",
			ConstLabels: prometheus.Labels{"service": service},
		}, func() float64 { return float64(runtime.NumGoroutine()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "chargepilot_heap_in_use_bytes", Help: "Bytes of heap in use.",
			ConstLabels: prometheus.Labels{"service": service},
		}, func() float64 { var mem runtime.MemStats; runtime.ReadMemStats(&mem); return float64(mem.HeapInuse) }),
	)
	return &Metrics{requests: requests, duration: duration, handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{})}
}

func (m *Metrics) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		begin := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		m.requests.WithLabelValues(c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Inc()
		m.duration.WithLabelValues(c.Request.Method, route).Observe(time.Since(begin).Seconds())
	}
}

func (m *Metrics) Register(router *gin.Engine) {
	router.GET("/metrics", func(c *gin.Context) { m.handler.ServeHTTP(c.Writer, c.Request) })
}
