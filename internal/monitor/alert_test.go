package monitor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

func TestWebhookNotifierRetriesWithBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("alert body is not JSON: %v", err)
		}
		for _, key := range []string{"monitor", "run", "workspaceId"} {
			if _, ok := payload[key]; !ok {
				t.Errorf("alert body missing %q: %s", key, body)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	webhook := server.URL
	notifier := &WebhookNotifier{
		Client:    server.Client(),
		Attempts:  3,
		BaseDelay: time.Millisecond,
		MaxDelay:  2 * time.Millisecond,
	}
	monitor := &entity.Monitor{ID: uuid.New(), WorkspaceID: "ws_1", AlertWebhook: &webhook}
	run := &entity.MonitorRun{ID: uuid.New(), MonitorID: monitor.ID, OK: false}

	if err := notifier.Notify(context.Background(), monitor, run); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestWebhookNotifierExhaustsRetries(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	webhook := server.URL
	notifier := &WebhookNotifier{
		Client:    server.Client(),
		Attempts:  2,
		BaseDelay: time.Millisecond,
		MaxDelay:  time.Millisecond,
	}
	err := notifier.Notify(context.Background(), &entity.Monitor{AlertWebhook: &webhook}, &entity.MonitorRun{})
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("error = %v, want retry exhaustion", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestWebhookNotifierSkipsMissingAndRejectsInvalid(t *testing.T) {
	notifier := NewWebhookNotifier()
	if err := notifier.Notify(context.Background(), &entity.Monitor{}, &entity.MonitorRun{}); err != nil {
		t.Fatalf("no webhook should be a no-op, got %v", err)
	}

	for _, target := range []string{"ftp://example.test/hook", "://bad", "relative/path"} {
		webhook := target
		err := notifier.Notify(context.Background(), &entity.Monitor{AlertWebhook: &webhook}, &entity.MonitorRun{})
		if err == nil {
			t.Fatalf("webhook %q unexpectedly accepted", target)
		}
	}
}
