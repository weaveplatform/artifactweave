package outbound

import (
	"net"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// ProxyRedirectHosts validates a proxy redirectedToHosts list and returns its
// canonical host keys; malformed lists fail with ErrConfiguration and no grant.
// ProxyDescriptor and ProxyConfig split only these four separators. Host tokens
// are exact DNS names or IP literals; treating URLs or wildcards as patterns
// would turn malformed configuration into a credential grant. Consumers own
// whether a listed host receives anything: the key grants no reachability.
func ProxyRedirectHosts(raw string) (map[string]bool, error) {
	hosts := make(map[string]bool)
	// ProxyConfig.setRedirectedToHosts uses StringUtils.trim (characters <= 32).
	// Unicode whitespace must remain invalid rather than widening a host grant.
	raw = strings.TrimFunc(raw, func(c rune) bool { return c <= ' ' })
	for _, token := range strings.FieldsFunc(raw, func(c rune) bool {
		return c == '\n' || c == ' ' || c == ',' || c == ';'
	}) {
		host := ProxyRedirectHost(token)
		if host == "" {
			return nil, ErrConfiguration
		}
		hosts[host] = true
	}
	return hosts, nil
}

// ProxyRedirectHost returns host's ProxyRedirectHosts key, or "" when invalid.
func ProxyRedirectHost(host string) string {
	// AuthScope uses Java's full root-locale lowercasing. Simple rune casing
	// would turn İ into ASCII i and widen the scope; full casing retains its dot.
	// Casers may be stateful, so each invocation owns its own value.
	key := cases.Lower(language.Und).String(host)
	if net.ParseIP(host) != nil {
		return key
	}
	// AuthScope compares lowercased host strings, not resolved DNS/IP identity.
	// A terminal dot is valid DNS syntax but remains part of the credential key.
	host = strings.TrimSuffix(key, ".")
	if len(host) == 0 || len(host) > 253 {
		return ""
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return ""
			}
		}
	}
	return key
}
