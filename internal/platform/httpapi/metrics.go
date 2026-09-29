package httpapi

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// A minimal Prometheus text-exposition endpoint.
//
// The project deliberately keeps its dependency set small, and the exposition
// format is simple enough to emit directly. This is not a general metrics
// library: it covers the counters, gauges and latency histogram that actually
// answer "is this service healthy and how is it doing", which is what the
// alerting question here needs.
//
// The important subtlety is the route label. Paths in this project embed
// identifiers — device_id, order_no, bill id — so labelling by the raw path
// would create one time series per request and take the scrape down. The
// matched route template is used instead, which bounds the cardinality by the
// number of registered routes.

const (
	serviceInfoMetric = "chargepilot_service_info"
	requestCount      = "chargepilot_http_requests_total"
	requestDuration   = "chargepilot_http_request_duration_seconds"
	buildInfo         = "chargepilot_build_info"
)

// durationBuckets are in seconds. They span a fast JSON read through to a
// settlement or export that can take seconds.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type seriesKey struct {
	method string
	route  string
	status int
}

type histogram struct {
	counts []uint64
	sum    float64
	total  uint64
}

// Metrics collects request counters and a latency histogram. The zero value is
// not usable; build one with NewMetrics.
type Metrics struct {
	service string

	mu        sync.Mutex
	counters  map[seriesKey]uint64
	histogram map[string]*histogram
	started   time.Time
}

// NewMetrics returns a registry for the named service.
func NewMetrics(service string) *Metrics {
	return &Metrics{
		service:   service,
		counters:  map[seriesKey]uint64{},
		histogram: map[string]*histogram{},
		started:   time.Now(),
	}
}

// Middleware records every handled request.
func (m *Metrics) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		begin := time.Now()
		c.Next()
		// FullPath is the matched route template, for example
		// /api/v1/internal/devices/:device_id/telemetry. It is empty for a
		// request that matched no route, which is collapsed to a single label
		// rather than to the raw path.
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		m.observe(c.Request.Method, route, c.Writer.Status(), time.Since(begin))
	}
}

func (m *Metrics) observe(method, route string, status int, elapsed time.Duration) {
	seconds := elapsed.Seconds()
	key := seriesKey{method: method, route: route, status: status}
	histKey := method + "\x00" + route

	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[key]++
	hist, present := m.histogram[histKey]
	if !present {
		hist = &histogram{counts: make([]uint64, len(durationBuckets))}
		m.histogram[histKey] = hist
	}
	for i, bound := range durationBuckets {
		if seconds <= bound {
			hist.counts[i]++
		}
	}
	hist.sum += seconds
	hist.total++
}

// Register installs the /metrics route. It is deliberately unauthenticated so
// a scraper does not need an operator session, and it exposes no business data
// beyond route names, status codes and timings.
func (m *Metrics) Register(router *gin.Engine) {
	router.GET("/metrics", m.serve)
}

func (m *Metrics) serve(c *gin.Context) {
	var b strings.Builder
	b.Grow(4096)

	fmt.Fprintf(&b, "# HELP %s Always 1; the service this scrape came from.\n", serviceInfoMetric)
	fmt.Fprintf(&b, "# TYPE %s gauge\n", serviceInfoMetric)
	fmt.Fprintf(&b, "%s{service=%q} 1\n", serviceInfoMetric, m.service)

	fmt.Fprintf(&b, "# HELP %s Build and start information.\n", buildInfo)
	fmt.Fprintf(&b, "# TYPE %s gauge\n", buildInfo)
	fmt.Fprintf(&b, "%s{service=%q,go_version=%q} 1\n", buildInfo, m.service, runtime.Version())
	fmt.Fprintf(&b, "# HELP chargepilot_uptime_seconds Seconds since this process started.\n# TYPE chargepilot_uptime_seconds counter\n")
	fmt.Fprintf(&b, "chargepilot_uptime_seconds{service=%q} %.0f\n", m.service, time.Since(m.started).Seconds())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(&b, "# HELP chargepilot_goroutines Number of goroutines.\n# TYPE chargepilot_goroutines gauge\n")
	fmt.Fprintf(&b, "chargepilot_goroutines{service=%q} %d\n", m.service, runtime.NumGoroutine())
	fmt.Fprintf(&b, "# HELP chargepilot_heap_in_use_bytes Bytes of heap in use.\n# TYPE chargepilot_heap_in_use_bytes gauge\n")
	fmt.Fprintf(&b, "chargepilot_heap_in_use_bytes{service=%q} %d\n", m.service, mem.HeapInuse)

	m.mu.Lock()
	defer m.mu.Unlock()

	keys := make([]seriesKey, 0, len(m.counters))
	for key := range m.counters {
		keys = append(keys, key)
	}
	// Sorted so a scrape is stable and two scrapes can be diffed by eye.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].status < keys[j].status
	})

	fmt.Fprintf(&b, "# HELP %s Handled HTTP requests.\n# TYPE %s counter\n", requestCount, requestCount)
	for _, key := range keys {
		fmt.Fprintf(&b, "%s{method=%q,route=%q,status=%q} %d\n", requestCount,
			key.method, key.route, strconv.Itoa(key.status), m.counters[key])
	}

	fmt.Fprintf(&b, "# HELP %s HTTP request latency in seconds.\n# TYPE %s histogram\n", requestDuration, requestDuration)
	histKeys := make([]string, 0, len(m.histogram))
	for key := range m.histogram {
		histKeys = append(histKeys, key)
	}
	sort.Strings(histKeys)
	for _, key := range histKeys {
		hist := m.histogram[key]
		method, route, _ := strings.Cut(key, "\x00")
		for i, bound := range durationBuckets {
			fmt.Fprintf(&b, "%s_bucket{method=%q,route=%q,le=%q} %d\n", requestDuration,
				method, route, strconv.FormatFloat(bound, 'g', -1, 64), hist.counts[i])
		}
		fmt.Fprintf(&b, "%s_bucket{method=%q,route=%q,le=\"+Inf\"} %d\n", requestDuration, method, route, hist.total)
		fmt.Fprintf(&b, "%s_sum{method=%q,route=%q} %g\n", requestDuration, method, route, hist.sum)
		fmt.Fprintf(&b, "%s_count{method=%q,route=%q} %d\n", requestDuration, method, route, hist.total)
	}

	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
}
