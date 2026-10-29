package httpclient

import (
	"context"
	"fmt"
	"net"
)

// lookupIPAddr is a seam for tests; production uses the system resolver.
var lookupIPAddr = net.DefaultResolver.LookupIPAddr

// guardedDialContext resolves the target host once, rejects it when any
// resolved address is internal, and then dials the validated IP directly.
//
// Resolving and dialling the same IP is what closes the DNS-rebinding hole:
// checking a hostname and letting the transport resolve it again would allow a
// second answer to point at an internal address (TOCTOU).
func guardedDialContext(dialer *net.Dialer, allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("httpclient: SSRF protection: invalid address %q: %w", addr, err)
		}

		ips, err := lookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("httpclient: resolve %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("httpclient: SSRF protection: no addresses for %q", host)
		}

		if !allowPrivate {
			for _, ip := range ips {
				if isInternalIP(ip.IP) {
					return nil, fmt.Errorf("httpclient: SSRF protection: %q resolves to internal address %s", host, ip.IP)
				}
			}
		}

		var lastErr error
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// isInternalIP reports whether ip must not be reachable by outbound calls:
// loopback, private (incl. CGNAT), link-local, unspecified, and multicast.
func isInternalIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}
