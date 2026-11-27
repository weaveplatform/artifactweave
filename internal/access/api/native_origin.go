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
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.String() != origin || strings.ToLower(u.Host) != u.Host || strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
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
