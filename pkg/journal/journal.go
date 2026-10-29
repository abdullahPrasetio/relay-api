// Package journal provides a request-scoped record that aggregates, for a single
// HTTP request or consumed message:
//
//   - every outbound third-party call (full request/response, capped & redacted)
//   - every custom trace point injected by application code
//
// The aggregated arrays are embedded into the parent record (api.log / consumer.log)
// by the access-log middleware / consumer loop, and each third-party call and trace
// point is ALSO written to its own file (thirdparty.log / trace.log) at the moment
// it happens — see AddThirdParty / AddTrace.
package journal

import (
	"context"
	"sync"
	"time"

	applogger "github.com/abdullahPrasetio/relay-api/pkg/logger"
)

// Kind identifies the parent record a journal belongs to.
type Kind string

const (
	KindAPI      Kind = "api"
	KindConsumer Kind = "consumer"
)

// ThirdParty captures a single outbound call made while handling a request/message.
type ThirdParty struct {
	Name         string    `json:"name,omitempty"`
	Method       string    `json:"method"`
	URL          string    `json:"url"`
	Host         string    `json:"host,omitempty"`
	Status       int       `json:"status"`
	LatencyMS    int64     `json:"latency_ms"`
	RequestBody  string    `json:"request_body,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	Error        string    `json:"error,omitempty"`
	StartedAt    time.Time `json:"started_at"`
}

// Trace is a custom trace point injected by application code via AddTrace.
type Trace struct {
	Name string    `json:"name"`
	Data any       `json:"data,omitempty"`
	At   time.Time `json:"at"`
}

// Journal aggregates third-party calls and traces for one request/message.
// All methods are safe for concurrent use and are nil-safe (a nil *Journal is a no-op),
// so handlers can call journal.FromContext(ctx).AddTrace(...) unconditionally.
type Journal struct {
	mu         sync.Mutex
	kind       Kind
	requestID  string
	traceID    string
	thirdParty []ThirdParty
	traces     []Trace
}

type ctxKey struct{}

// Start creates a Journal of the given kind and stores it in the returned context.
func Start(ctx context.Context, kind Kind) (context.Context, *Journal) {
	j := &Journal{kind: kind}
	return context.WithValue(ctx, ctxKey{}, j), j
}

// Kind returns the parent record type (api / consumer).
func (j *Journal) Kind() Kind {
	if j == nil {
		return ""
	}
	return j.kind
}

// RequestID returns the correlation request id.
func (j *Journal) RequestID() string {
	if j == nil {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.requestID
}

// TraceID returns the APM/OTel trace id recorded via SetTraceID.
func (j *Journal) TraceID() string {
	if j == nil {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.traceID
}

// FromContext returns the Journal stored in ctx, or nil when none is present.
func FromContext(ctx context.Context) *Journal {
	j, _ := ctx.Value(ctxKey{}).(*Journal)
	return j
}

// SetRequestID records the correlation request id (also used in child file lines).
func (j *Journal) SetRequestID(id string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.requestID = id
	j.mu.Unlock()
}

// SetTraceID records the APM/OTel trace id for cross-correlation.
func (j *Journal) SetTraceID(id string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.traceID = id
	j.mu.Unlock()
}

// AddThirdParty appends tp to the parent record and writes a standalone JSON line
// to thirdparty.log.
func (j *Journal) AddThirdParty(tp ThirdParty) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.thirdParty = append(j.thirdParty, tp)
	rid, tid := j.requestID, j.traceID
	j.mu.Unlock()

	applogger.ThirdParty().Info().
		Str("request_id", rid).
		Str("trace_id", tid).
		Str("name", tp.Name).
		Str("method", tp.Method).
		Str("url", tp.URL).
		Str("host", tp.Host).
		Int("status", tp.Status).
		Int64("latency_ms", tp.LatencyMS).
		Str("error", tp.Error).
		Msg("thirdparty")
}

// AddTrace appends a custom trace point to the parent record and writes a standalone
// JSON line to trace.log. This is the "inject function" entry point for app code.
func (j *Journal) AddTrace(name string, data any) {
	if j == nil {
		return
	}
	t := Trace{Name: name, Data: data, At: time.Now()}
	j.mu.Lock()
	j.traces = append(j.traces, t)
	rid, tid := j.requestID, j.traceID
	j.mu.Unlock()

	applogger.Trace().Info().
		Str("request_id", rid).
		Str("trace_id", tid).
		Str("name", name).
		Interface("data", data).
		Msg("trace")
}

// ThirdParties returns a copy of the aggregated third-party calls.
func (j *Journal) ThirdParties() []ThirdParty {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]ThirdParty, len(j.thirdParty))
	copy(out, j.thirdParty)
	return out
}

// Traces returns a copy of the aggregated custom trace points.
func (j *Journal) Traces() []Trace {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Trace, len(j.traces))
	copy(out, j.traces)
	return out
}
