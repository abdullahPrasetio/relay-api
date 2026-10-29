package observability

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"

	"github.com/gofiber/fiber/v2"
)

// registry is a package-level Prometheus registry so tests can create isolated ones.
var registry = prometheus.NewRegistry()

var (
	httpRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wapgo",
		Name:      "http_requests_total",
		Help:      "Total HTTP requests partitioned by route, method, and status code.",
	}, []string{"route", "method", "status"})

	httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "wapgo",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"route", "method"})
)

func init() {
	registry.MustRegister(httpRequestsTotal, httpRequestDuration)
	// Also register default Go runtime / process collectors.
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// MetricsMiddleware records RED (Rate, Errors, Duration) metrics for every request.
// It should be placed after the route registration so c.Route().Path is populated.
func MetricsMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()

		route := c.Route().Path
		method := c.Method()
		status := strconv.Itoa(c.Response().StatusCode())

		httpRequestsTotal.WithLabelValues(route, method, status).Inc()
		httpRequestDuration.WithLabelValues(route, method).Observe(time.Since(start).Seconds())

		return err
	}
}

// MetricsHandler returns a Fiber handler that serves the Prometheus /metrics endpoint.
// In production this endpoint MUST be protected (see route/router.go).
func MetricsHandler() fiber.Handler {
	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{Registry: registry})
	return func(c *fiber.Ctx) error {
		fasthttpadaptor.NewFastHTTPHandler(h)(c.Context())
		return nil
	}
}
