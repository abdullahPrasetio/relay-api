package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	apmfiber "go.elastic.co/apm/module/apmfiber/v2"
	apmhttp "go.elastic.co/apm/module/apmhttp/v2"
	apmotel "go.elastic.co/apm/module/apmotel/v2"
	"go.elastic.co/apm/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/gorm"
)

// elasticProvider implements Provider using the Elastic APM Go agent.
//
// The agent reads its config automatically from environment variables:
//
//	ELASTIC_APM_SERVER_URL     — APM Server endpoint (required)
//	ELASTIC_APM_SERVICE_NAME   — overrides the service name (optional)
//	ELASTIC_APM_SECRET_TOKEN   — auth token (optional)
//	ELASTIC_APM_ENVIRONMENT    — e.g. "production" (optional)
//	ELASTIC_APM_ACTIVE         — set "false" to disable (default true)
//
// All methods use apm.DefaultTracer() which is automatically configured from ENV.
type elasticProvider struct{}

func newElasticProvider() (Provider, error) {
	// Bridge OpenTelemetry → Elastic APM: install an apmotel TracerProvider as the
	// global OTel provider so that spans created via otel.Tracer(...) (e.g. in the
	// generated usecases) are forwarded to Elastic APM and nested under the
	// apmfiber transaction. Without this bridge those spans would be dropped.
	tp, err := apmotel.NewTracerProvider()
	if err != nil {
		return nil, fmt.Errorf("apmotel tracer provider: %w", err)
	}
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return &elasticProvider{}, nil
}

func (p *elasticProvider) HTTPMiddleware() fiber.Handler {
	return apmfiber.Middleware()
}

func (p *elasticProvider) InstrumentGORM(db *gorm.DB) error {
	return db.Use(&elasticGORMPlugin{})
}

func (p *elasticProvider) InstrumentRedis(client *redis.Client) {
	client.AddHook(&elasticRedisHook{})
}

func (p *elasticProvider) WrapTransport(inner http.RoundTripper) http.RoundTripper {
	return apmhttp.WrapRoundTripper(inner)
}

func (p *elasticProvider) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		apm.DefaultTracer().Flush(nil)
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ── GORM plugin ──────────────────────────────────────────────────────────────

type elasticGORMPlugin struct{}

func (p *elasticGORMPlugin) Name() string { return "elastic_apm" }

func (p *elasticGORMPlugin) Initialize(db *gorm.DB) error {
	before := func(op string) func(*gorm.DB) {
		return func(db *gorm.DB) {
			if db.Statement == nil || db.Statement.Context == nil {
				return
			}
			span, ctx := apm.StartSpan(db.Statement.Context, "db."+op, "db.sql")
			db.Statement.Context = ctx
			db.Statement.Settings.Store(elasticGORMSpanKey{}, span)
		}
	}
	after := func(db *gorm.DB) {
		if db.Statement == nil {
			return
		}
		v, _ := db.Statement.Settings.LoadAndDelete(elasticGORMSpanKey{})
		span, ok := v.(*apm.Span)
		if !ok {
			return
		}
		if db.Error != nil && !errors.Is(db.Error, gorm.ErrRecordNotFound) {
			apm.CaptureError(db.Statement.Context, db.Error).Send()
		}
		span.End()
	}

	db.Callback().Query().Before("gorm:query").Register("apm:before_query", before("query"))
	db.Callback().Query().After("gorm:after_query").Register("apm:after_query", after)

	db.Callback().Create().Before("gorm:create").Register("apm:before_create", before("create"))
	db.Callback().Create().After("gorm:after_create").Register("apm:after_create", after)

	db.Callback().Update().Before("gorm:update").Register("apm:before_update", before("update"))
	db.Callback().Update().After("gorm:after_update").Register("apm:after_update", after)

	db.Callback().Delete().Before("gorm:delete").Register("apm:before_delete", before("delete"))
	db.Callback().Delete().After("gorm:after_delete").Register("apm:after_delete", after)

	db.Callback().Row().Before("gorm:row").Register("apm:before_row", before("row"))
	db.Callback().Row().After("gorm:after_row").Register("apm:after_row", after)

	db.Callback().Raw().Before("gorm:raw").Register("apm:before_raw", before("raw"))
	db.Callback().Raw().After("gorm:after_raw").Register("apm:after_raw", after)

	return nil
}

type elasticGORMSpanKey struct{}

// ── Redis hook ───────────────────────────────────────────────────────────────

// elasticRedisHook instruments go-redis v9 commands as Elastic APM exit spans.
type elasticRedisHook struct {
	mu sync.Mutex
}

func (h *elasticRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *elasticRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		span, ctx := apm.StartSpanOptions(ctx, "redis."+cmd.Name(), "db.redis", apm.SpanOptions{
			ExitSpan: true,
		})
		span.Context.SetDatabase(apm.DatabaseSpanContext{Type: "redis", Statement: cmd.String()})
		err := next(ctx, cmd)
		if err != nil && !errors.Is(err, redis.Nil) {
			apm.CaptureError(ctx, err).Send()
		}
		span.End()
		return err
	}
}

func (h *elasticRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		span, ctx := apm.StartSpanOptions(ctx, "redis.pipeline", "db.redis", apm.SpanOptions{
			ExitSpan: true,
		})
		err := next(ctx, cmds)
		span.End()
		return err
	}
}
