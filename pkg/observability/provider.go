package observability

import (
	"context"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Provider is the unified observability backend interface.
// Use New() to obtain an implementation based on OBSERVABILITY_PROVIDER config.
//
// Two backends are available:
//   - "otel"        — OpenTelemetry SDK (default)
//   - "elastic_apm" — Elastic APM Go agent
//
// Prometheus RED metrics (MetricsMiddleware / MetricsHandler) are always active
// and are independent of the chosen Provider.
type Provider interface {
	// HTTPMiddleware returns a Fiber handler that creates a server-side span for
	// every incoming request and propagates trace context to downstream calls.
	HTTPMiddleware() fiber.Handler

	// InstrumentGORM registers query tracing callbacks on db.
	// Call once immediately after database.NewConnection().
	InstrumentGORM(db *gorm.DB) error

	// InstrumentRedis attaches command tracing hooks to the Redis client.
	// Call once immediately after creating the redis.Client.
	InstrumentRedis(client *redis.Client)

	// WrapTransport wraps an http.RoundTripper so that outgoing HTTP requests
	// carry trace context and are recorded as child spans.
	// Pass the result as httpclient.Options.TransportWrapper.
	WrapTransport(inner http.RoundTripper) http.RoundTripper

	// Shutdown flushes buffered spans/transactions and releases resources.
	// Must be called during graceful shutdown.
	Shutdown(ctx context.Context) error
}

// noopProvider is used when no backend is configured or in tests.
type noopProvider struct{}

func (noopProvider) HTTPMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error { return c.Next() }
}
func (noopProvider) InstrumentGORM(_ *gorm.DB) error                         { return nil }
func (noopProvider) InstrumentRedis(_ *redis.Client)                         {}
func (noopProvider) WrapTransport(inner http.RoundTripper) http.RoundTripper { return inner }
func (noopProvider) Shutdown(_ context.Context) error                        { return nil }
