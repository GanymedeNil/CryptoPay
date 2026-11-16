package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedTarget is wrapped by every error caused by the SSRF guard.
var ErrBlockedTarget = errors.New("blocked target address")

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // CGNAT
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"240.0.0.0/4",     // reserved + broadcast
	"64:ff9b::/96",    // NAT64 (the embedded IPv4 is judged instead)
	"100::/64",        // discard-only
	"2001:db8::/32",   // documentation
	"fec0::/10",       // deprecated site-local
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// isBlockedIP reports whether a webhook must never be sent to ip:
// loopback, private (RFC 1918 / ULA), link-local (incl. cloud metadata
// 169.254.169.254), CGNAT, multicast, unspecified and other reserved ranges.
// IPv4-mapped IPv6 addresses are judged as their IPv4 form.
func isBlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			if ip.Is6() && p.Bits() == 96 { // NAT64: judge the embedded IPv4
				b := ip.As16()
				return isBlockedIP(netip.AddrFrom4([4]byte(b[12:])))
			}
			return true
		}
	}
	return false
}

// lookupFunc resolves a host name to IP addresses.
type lookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)

func netLookup(r *net.Resolver) lookupFunc {
	if r == nil {
		r = net.DefaultResolver
	}
	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		ips, err := r.LookupNetIP(ctx, "ip", host)
		return ips, err
	}
}

// guardedDialContext returns a DialContext that resolves the host itself,
// rejects blocked addresses and dials the vetted IP directly, so a DNS
// answer cannot change between check and connect (DNS rebinding). The
// dialer's Control hook re-checks the final socket address as a second line
// of defence.
func guardedDialContext(lookup lookupFunc, timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: unparsable address %q", ErrBlockedTarget, address)
			}
			if isBlockedIP(ap.Addr()) {
				return fmt.Errorf("%w: %s", ErrBlockedTarget, ap.Addr())
			}
			return nil
		},
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var ips []netip.Addr
		if ip, perr := netip.ParseAddr(host); perr == nil {
			ips = []netip.Addr{ip}
		} else {
			ips, err = lookup(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", host, err)
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("resolve %s: no addresses", host)
		}
		// Every address must be allowed: a mixed answer is suspicious.
		for _, ip := range ips {
			if isBlockedIP(ip) {
				return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedTarget, host, ip.Unmap())
			}
		}
		var lastErr error
		for _, ip := range ips {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return c, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, lastErr
	}
}
