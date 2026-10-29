package httpclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubResolver replaces the system resolver for one test.
func stubResolver(t *testing.T, fn func(context.Context, string) ([]net.IPAddr, error)) *atomic.Int32 {
	t.Helper()
	old := lookupIPAddr
	t.Cleanup(func() { lookupIPAddr = old })
	var calls atomic.Int32
	lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		calls.Add(1)
		return fn(ctx, host)
	}
	return &calls
}

func ipAddr(s string) net.IPAddr { return net.IPAddr{IP: net.ParseIP(s)} }

func TestGuardedDialBlocksResolvedInternal(t *testing.T) {
	stubResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{ipAddr("10.1.2.3")}, nil
	})

	_, err := guardedDialContext(&net.Dialer{}, false)(context.Background(), "tcp", "public.example:80")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves to internal address 10.1.2.3")
}

func TestGuardedDialBlocksWhenAnyAddressIsInternal(t *testing.T) {
	stubResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{ipAddr("93.184.216.34"), ipAddr("127.0.0.1")}, nil
	})

	_, err := guardedDialContext(&net.Dialer{}, false)(context.Background(), "tcp", "mixed.example:443")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves to internal address 127.0.0.1")
}

func TestGuardedDialDialsResolvedIPOnce(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan struct{})
	go func() {
		conn, aerr := ln.Accept()
		if aerr == nil {
			_ = conn.Close()
			close(accepted)
		}
	}()

	listenHost, listenPort, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	calls := stubResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{ipAddr(listenHost)}, nil // port is re-added by the dialer
	})

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := guardedDialContext(dialer, true)(context.Background(), "tcp", net.JoinHostPort(listenHost, listenPort))
	require.NoError(t, err)
	_ = conn.Close()

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("listener never accepted the dial")
	}

	assert.Equal(t, int32(1), calls.Load(), "resolver must be called exactly once")
}

func TestGuardedDialAllowsPrivateWhenEnabled(t *testing.T) {
	stubResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{ipAddr("10.255.255.1")}, nil
	})

	dialer := &net.Dialer{Timeout: 50 * time.Millisecond}
	_, err := guardedDialContext(dialer, true)(context.Background(), "tcp", "selfhosted.internal:9000")
	// The dial itself fails (unreachable), but it must not be blocked as SSRF.
	if err != nil {
		assert.NotContains(t, err.Error(), "SSRF")
	}
}

func TestGuardedDialRejectsAddressWithoutPort(t *testing.T) {
	_, err := guardedDialContext(&net.Dialer{}, false)(context.Background(), "tcp", "no-port")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid address")
}

func TestGuardedDialRejectsNoAddresses(t *testing.T) {
	stubResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return nil, nil
	})
	_, err := guardedDialContext(&net.Dialer{}, false)(context.Background(), "tcp", "empty.example:80")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no addresses")
}

func TestClientBlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secret")
	}))
	t.Cleanup(srv.Close)

	c := New(Options{MaxRetries: 1})
	_, _, err := c.Get(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSRF")
}

func TestClientAllowsLoopbackWhenPrivateAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	c := New(Options{MaxRetries: 1, AllowPrivate: true})
	resp, body, err := c.Get(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", string(body))
}

func TestIsInternalIP(t *testing.T) {
	internal := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1",
		"::1", "fc00::1", "fe80::1", "::",
	}
	for _, s := range internal {
		assert.True(t, isInternalIP(net.ParseIP(s)), "%s must be internal", s)
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		assert.False(t, isInternalIP(net.ParseIP(s)), "%s must be public", s)
	}
}
