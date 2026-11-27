package api

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ValidNativeOrigin admits an explicit canonical HTTPS origin. HTTP is limited
// to loopback development; forwarded and override headers never select it.
func ValidNativeOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.String() != origin ||
		strings.ToLower(u.Host) != u.Host ||
		strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || u.Scheme == "https" && n == 443 || u.Scheme == "http" && n == 80 {
			return false
		}
	}
	if !canonicalNativeHost(u.Hostname()) {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(u.Hostname())
	return err == nil && ip.IsLoopback()
}

// Browsers serialize an ASCII origin (RFC6454 section6.2). Their legacy IPv4
// parser also rewrites shortened, integer, octal and hex addresses; net/url does
// not. Admit canonical IP literals or ordinary ASCII DNS/punycode names only,
// without DNS resolution or accepting a browser-normalized alias as another origin.
func canonicalNativeHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && !ip.Is4In6() && ip.String() == host
	}
	for _, c := range host {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' {
			continue
		}
		return false
	}
	last := host[strings.LastIndex(host, ".")+1:]
	if last == "" {
		return false
	}
	if strings.Trim(last, "0123456789") == "" {
		return false
	}
	if strings.HasPrefix(last, "0x") && strings.Trim(last[2:], "0123456789abcdef") == "" {
		return false
	}
	return true
}
