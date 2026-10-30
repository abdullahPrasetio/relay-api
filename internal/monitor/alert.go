package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

// AlertNotifier delivers a failure alert. Implementations are best-effort:
// the executor logs delivery errors but never changes the stored run.
type AlertNotifier interface {
	Notify(ctx context.Context, monitor *entity.Monitor, run *entity.MonitorRun) error
}

// NoopNotifier is used when alerts are disabled or in tests.
type NoopNotifier struct{}

func (NoopNotifier) Notify(context.Context, *entity.Monitor, *entity.MonitorRun) error { return nil }

// WebhookNotifier POSTs {monitor, run, workspaceId} to the monitor's webhook
// with bounded exponential backoff.
type WebhookNotifier struct {
	Client    *http.Client
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// NewWebhookNotifier returns a notifier with production defaults (3 attempts,
// 500ms→2s backoff, 10s request timeout).
func NewWebhookNotifier() *WebhookNotifier {
	return &WebhookNotifier{
		Client:    &http.Client{Timeout: 10 * time.Second},
		Attempts:  3,
		BaseDelay: 500 * time.Millisecond,
		MaxDelay:  2 * time.Second,
	}
}

// Notify delivers one alert. A 2xx response is success; every other outcome is
// retried with exponential backoff until Attempts is exhausted or ctx ends.
func (n *WebhookNotifier) Notify(ctx context.Context, monitor *entity.Monitor, run *entity.MonitorRun) error {
	if monitor == nil || monitor.AlertWebhook == nil || strings.TrimSpace(*monitor.AlertWebhook) == "" {
		return nil
	}
	target := strings.TrimSpace(*monitor.AlertWebhook)
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid alert webhook URL")
	}

	body, err := json.Marshal(map[string]any{
		"monitor":     monitor,
		"run":         run,
		"workspaceId": monitor.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("encode alert body: %w", err)
	}

	attempts := n.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			delay := n.backoff(attempt)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}

		lastErr = n.post(ctx, parsed.String(), body)
		if lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("alert webhook failed after %d attempts: %w", attempts, lastErr)
}

func (n *WebhookNotifier) post(ctx context.Context, target string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req) // #nosec G107 -- operator-configured webhook URL
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (n *WebhookNotifier) backoff(attempt int) time.Duration {
	base := n.BaseDelay
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	delay := base << (attempt - 1)
	max := n.MaxDelay
	if max <= 0 {
		max = 2 * time.Second
	}
	if delay > max {
		delay = max
	}
	return delay
}

var _ AlertNotifier = (*WebhookNotifier)(nil)
