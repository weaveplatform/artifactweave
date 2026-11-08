package remote

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// destinationPolicy grants private-network access only to authorities configured
// by the operator. DNS answers are validated together, then used directly by the
// transport: a second lookup must not replace a checked public IP with a private IP.
type destinationPolicy struct {
	private map[string]bool
	lookup  func(context.Context, string) ([]net.IPAddr, error)
}

func (p *destinationPolicy) addresses(ctx context.Context, u *url.URL) ([]string, error) {
	if _, err := CheckedURL(u.String()); err != nil {
		return nil, err
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
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, safeError(err)
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
		if forbiddenAddress(a) || (!p.private[Origin(u)] && !publicAddress(a)) {
			return nil, ErrSecurity
		}
		addresses = append(addresses, net.JoinHostPort(a.String(), Port(u)))
	}
	return addresses, nil
}

func forbiddenAddress(ip netip.Addr) bool {
	return ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip == netip.MustParseAddr("100.100.100.200") ||
		ip == netip.MustParseAddr("168.63.129.16") ||
		ip == netip.MustParseAddr("fd00:ec2::254")
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix(
		"2001::/32",
	),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func publicAddress(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
