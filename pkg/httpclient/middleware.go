package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sony/gobreaker"
)

type retryTransport struct {
	inner      http.RoundTripper
	maxRetries int
	baseDelay  time.Duration
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var (
		lastErr    error
		lastStatus int
	)

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if attempt > 0 {
			if req.Body != nil && req.GetBody == nil {
				break
			}
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("httpclient: retry reset body: %w", err)
				}
				req.Body = body
			}

			delay := t.baseDelay * time.Duration(math.Pow(2, float64(attempt-1)))
			timer := time.NewTimer(delay)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			case <-timer.C:
			}
		}

		resp, err := t.inner.RoundTrip(req)
		if err != nil {
			if isRetryableErr(err) {
				lastErr = err
				continue
			}
			return nil, err
		}
		if resp.StatusCode < 500 {
			return resp, nil
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		lastStatus = resp.StatusCode
	}

	if lastErr != nil {
		return nil, fmt.Errorf("httpclient: transport error after %d retries: %w", t.maxRetries, lastErr)
	}
	return nil, fmt.Errorf("httpclient: server returned %d after %d retries", lastStatus, t.maxRetries)
}

func isRetryableErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

type cbTransport struct {
	inner http.RoundTripper
	cb    *gobreaker.CircuitBreaker
}

func newCBTransport(inner http.RoundTripper, settings gobreaker.Settings) *cbTransport {
	return &cbTransport{inner: inner, cb: gobreaker.NewCircuitBreaker(settings)}
}

func (t *cbTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	result, err := t.cb.Execute(func() (any, error) {
		return t.inner.RoundTrip(req)
	})
	if err != nil {
		return nil, err
	}
	return result.(*http.Response), nil //nolint:forcetypeassert
}

type ssrfGuardTransport struct {
	inner        http.RoundTripper
	allowedHosts []string
	allowPrivate bool
}

func (t *ssrfGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateHost(req.URL.Hostname(), t.allowedHosts, t.allowPrivate); err != nil {
		return nil, err
	}
	return t.inner.RoundTrip(req)
}

func ssrfCheckRedirect(allowedHosts []string, allowPrivate bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("httpclient: stopped after 10 redirects")
		}
		return validateHost(req.URL.Hostname(), allowedHosts, allowPrivate)
	}
}

// validateHost returns an error when host is internal/loopback/link-local
// (unless allowPrivate), or is absent from allowedHosts (when the allowlist is
// non-empty). The dialer repeats the check on resolved IPs.
func validateHost(host string, allowedHosts []string, allowPrivate bool) error {
	if host == "" {
		return fmt.Errorf("httpclient: SSRF protection: empty host")
	}
	if !allowPrivate && isInternalHost(host) {
		return fmt.Errorf("httpclient: SSRF protection: host %q is internal/loopback/link-local", host)
	}
	if len(allowedHosts) == 0 {
		return nil
	}
	for _, h := range allowedHosts {
		if strings.EqualFold(host, h) {
			return nil
		}
	}
	return fmt.Errorf("httpclient: SSRF protection: host %q not in allowlist", host)
}

var privateRanges = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, n, _ := net.ParseCIDR(cidr)
		nets = append(nets, n)
	}
	return nets
}()

func isInternalHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "ip6-localhost", "ip6-loopback":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}
