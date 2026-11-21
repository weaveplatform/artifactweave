package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Safe transport classifications, independent of business response mapping.
var (
	ErrConfiguration = errors.New("outbound: invalid transport configuration")
	ErrSecurity      = errors.New("outbound: destination or TLS refused")
	ErrConnection    = errors.New("outbound: connection unavailable")
	ErrRedirect      = errors.New("outbound: redirect refused")
	ErrRequest       = errors.New("outbound: request failed")
	ErrClosed        = errors.New("outbound: client closed")
)

// Policy retains immutable exact-origin grants and validates every DNS answer.
// It does not authorize business paths, headers, credentials or HTTP methods.
type Policy struct {
	private map[string]bool
	lookup  func(context.Context, string) ([]net.IPAddr, error)
}

// NewPolicy snapshots private-network grants without performing DNS or I/O.
func NewPolicy(privateOrigins []string, lookup func(context.Context, string) ([]net.IPAddr, error)) (*Policy, error) {
	p := &Policy{private: make(map[string]bool, len(privateOrigins)), lookup: lookup}
	if p.lookup == nil {
		p.lookup = net.DefaultResolver.LookupIPAddr
	}
	for _, raw := range privateOrigins {
		u, err := url.Parse(raw)
		if err != nil || checkAuthority(u) != nil {
			return nil, ErrConfiguration
		}
		p.private[Origin(u)] = true
	}
	return p, nil
}

// Origin canonicalizes the network authority, including an implied port. It
// expects a validated HTTP URL; callers must not use it as URL validation.
func Origin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), port(u))
}

func port(u *url.URL) string {
	if value := u.Port(); value != "" {
		return value
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func checkAuthority(u *url.URL) error {
	if u == nil || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(u.Hostname(), "%\\\x00\r\n") {
		return ErrSecurity
	}
	if _, err := url.Parse(u.String()); err != nil {
		return ErrSecurity
	}
	n, err := strconv.Atoi(port(u))
	if err != nil || n < 1 || n > 65535 {
		return ErrSecurity
	}
	return nil
}

// Addresses checks the authority and all DNS answers together. Consumers dial
// these exact addresses while retaining the logical HTTP Host and TLS name.
func (p *Policy) Addresses(ctx context.Context, u *url.URL) ([]string, error) {
	if p == nil || checkAuthority(u) != nil {
		return nil, ErrSecurity
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	switch host {
	case "metadata.google.internal", "metadata.goog", "instance-data":
		return nil, ErrSecurity
	}
	var ips []net.IPAddr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []net.IPAddr{{IP: net.IP(ip.AsSlice())}}
	} else {
		var err error
		ips, err = p.lookup(ctx, host)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, context.DeadlineExceeded
			}
			return nil, ErrConnection
		}
	}
	if len(ips) == 0 {
		return nil, ErrConnection
	}
	addresses := make([]string, 0, len(ips))
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip.IP)
		if !ok || ip.Zone != "" {
			return nil, ErrSecurity
		}
		a = a.Unmap()
		if forbidden(a) || !p.private[Origin(u)] && !public(a) {
			return nil, ErrSecurity
		}
		addresses = append(addresses, net.JoinHostPort(a.String(), port(u)))
	}
	return addresses, nil
}

func forbidden(ip netip.Addr) bool {
	return ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip == netip.MustParseAddr("100.100.100.200") || ip == netip.MustParseAddr("168.63.129.16") || ip == netip.MustParseAddr("fd00:ec2::254")
}

// Keep the remote-cache destination exclusions in one platform-owned set so
// replication cannot accidentally permit a tunnel the read path refuses.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func public(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() {
		return false
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
