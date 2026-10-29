package observability

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/uptrace/opentelemetry-go-extra/otelgorm"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"gorm.io/gorm"

	applogger "github.com/abdullahPrasetio/relay-api/pkg/logger"
)

// otelProvider implements Provider using the OpenTelemetry SDK.
type otelProvider struct {
	tracer   trace.Tracer
	shutdown func(context.Context) error
}

// newOTelProvider initialises the global OTel TracerProvider and returns a Provider.
// serviceName / serviceVersion become service.name / service.version resource attributes.
// When enabled=false a no-op provider is installed (instrumentation code never panics
// but no spans are exported).
func newOTelProvider(ctx context.Context, cfg otelConfig) (Provider, error) {
	if !cfg.Enabled {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		return &otelProvider{
			tracer:   otel.Tracer(cfg.ServiceName),
			shutdown: func(context.Context) error { return nil },
		}, nil
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.TelemetrySDKLanguageGo,
			semconv.DeploymentEnvironment(cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	var exporter sdktrace.SpanExporter
	if cfg.OTLPEndpoint != "" {
		exporter, err = otlptracehttp.New(ctx,
			otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint),
		)
	} else {
		exporter, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
	}
	if err != nil {
		return nil, fmt.Errorf("otel exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &otelProvider{
		tracer:   otel.Tracer(cfg.ServiceName),
		shutdown: tp.Shutdown,
	}, nil
}

func (p *otelProvider) HTTPMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		carrier := &fiberHeaderCarrier{c: c}
		ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)

		route := c.Path()
		ctx, span := p.tracer.Start(ctx, c.Method()+" "+route,
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		if rid := applogger.RequestIDFromContext(c.Context()); rid != "" {
			span.SetAttributes(attribute.String("http.request_id", rid))
		}

		c.Locals("otel_ctx", ctx)

		err := c.Next()

		span.SetAttributes(
			semconv.HTTPRequestMethodKey.String(c.Method()),
			semconv.HTTPRouteKey.String(c.Route().Path),
			semconv.HTTPResponseStatusCode(c.Response().StatusCode()),
		)
		if err != nil {
			span.RecordError(err)
		}
		return err
	}
}

func (p *otelProvider) InstrumentGORM(db *gorm.DB) error {
	return db.Use(otelgorm.NewPlugin())
}

func (p *otelProvider) InstrumentRedis(client *redis.Client) {
	// redisotel.InstrumentTracing attaches hooks; errors are non-fatal config issues.
	_ = redisotel.InstrumentTracing(client)
}

func (p *otelProvider) WrapTransport(inner http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(inner)
}

func (p *otelProvider) Shutdown(ctx context.Context) error {
	return p.shutdown(ctx)
}

// otelConfig are the parameters needed to bootstrap the OTel provider.
type otelConfig struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	OTLPEndpoint   string
	Enabled        bool
}
