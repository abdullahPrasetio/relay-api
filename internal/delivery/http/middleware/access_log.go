package middleware

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"

	"github.com/abdullahPrasetio/relay-api/pkg/journal"
	applogger "github.com/abdullahPrasetio/relay-api/pkg/logger"
	"github.com/abdullahPrasetio/relay-api/pkg/observability"
)

// AccessLogOptions configures the full request/response access log.
type AccessLogOptions struct {
	BodyMaxBytes  int  // per-body cap in bytes (<=0 disables body capture)
	CaptureBodies bool // when false, request/response bodies become "[omitted]"
}

// AccessLog starts a request-scoped journal and, after the handler runs, writes one
// JSON line to api.log containing the FULL request (method, url, query, headers, body)
// and response (status, headers, body, latency), plus the aggregated thirdparty[] and
// trace[] arrays collected during the request and the correlating trace_id.
//
// Place it after the observability middleware (so a trace is active) and after
// RequestID (so the request id is available).
func AccessLog(opts AccessLogOptions) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		// Begin the journal so httpclient calls and AddTrace land in this request.
		ctx, j := journal.Start(c.UserContext(), journal.KindAPI)
		c.SetUserContext(ctx)

		rid := applogger.RequestIDFromContext(ctx)
		j.SetRequestID(rid)
		traceID := observability.TraceID(observability.TraceContext(c))
		j.SetTraceID(traceID)

		// Capture the request before the handler consumes it.
		reqHeaders := map[string]string{}
		c.Request().Header.VisitAll(func(k, v []byte) { reqHeaders[string(k)] = string(v) })
		reqBody := "[omitted]"
		if opts.CaptureBodies {
			reqBody = journal.CapBody(c.Body(), c.Get("Content-Type"), opts.BodyMaxBytes)
		}

		err := c.Next()

		latency := time.Since(start)
		// Trace id may only become resolvable after the handler chain ran.
		if traceID == "" {
			traceID = observability.TraceID(observability.TraceContext(c))
			j.SetTraceID(traceID)
		}

		respHeaders := map[string]string{}
		c.Response().Header.VisitAll(func(k, v []byte) { respHeaders[string(k)] = string(v) })
		respBody := "[omitted]"
		switch {
		case c.Response().IsBodyStream():
			// Reading a streamed body consumes/attaches the writer, which would
			// break SSE and other streaming responses.
			respBody = "[stream]"
		case opts.CaptureBodies:
			respBody = journal.CapBody(c.Response().Body(), string(c.Response().Header.ContentType()), opts.BodyMaxBytes)
		}

		applogger.API().Info().
			Str("request_id", rid).
			Str("trace_id", traceID).
			Str("client_ip", c.IP()).
			Dict("request", zerolog.Dict().
				Str("method", c.Method()).
				Str("url", c.OriginalURL()).
				Str("path", c.Path()).
				Str("query", string(c.Request().URI().QueryString())).
				Interface("headers", journal.RedactHeaders(reqHeaders)).
				Str("body", reqBody),
			).
			Dict("response", zerolog.Dict().
				Int("status", c.Response().StatusCode()).
				Interface("headers", journal.RedactHeaders(respHeaders)).
				Str("body", respBody).
				Dur("latency_ms", latency),
			).
			Interface("thirdparty", j.ThirdParties()).
			Interface("trace", j.Traces()).
			Msg("request")

		return err
	}
}
