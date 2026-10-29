package observability

import (
	"context"
	"fmt"

	"github.com/abdullahPrasetio/relay-api/config"
)

// New creates the observability Provider selected by cfg.Provider:
//   - "none"        — tracing disabled (no-op provider)
//   - "elastic_apm" — Elastic APM Go agent (reads ELASTIC_APM_* ENV vars natively)
//   - "otel" or ""  — OpenTelemetry SDK (default)
//
// serviceName, serviceVersion, and environment are used as OTel resource attributes.
// The Elastic APM agent reads its config from ELASTIC_APM_* ENV vars automatically.
func New(ctx context.Context, cfg *config.ObservabilityConfig, serviceName, serviceVersion, environment string) (Provider, error) {
	switch cfg.Provider {
	case "none":
		return noopProvider{}, nil
	case "elastic_apm":
		p, err := newElasticProvider()
		if err != nil {
			return nil, fmt.Errorf("elastic apm provider: %w", err)
		}
		return p, nil
	default: // "otel" or empty
		p, err := newOTelProvider(ctx, otelConfig{
			ServiceName:    serviceName,
			ServiceVersion: serviceVersion,
			Environment:    environment,
			OTLPEndpoint:   cfg.OTLPEndpoint,
			Enabled:        cfg.TracingEnabled,
		})
		if err != nil {
			return nil, fmt.Errorf("otel provider: %w", err)
		}
		return p, nil
	}
}
