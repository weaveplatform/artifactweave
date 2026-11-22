package upstream

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

// ProxyDescriptor and ProxyConfig split only these four separators. Host tokens
// are exact DNS names or IP literals; treating URLs or wildcards as patterns
// would turn malformed configuration into a credential grant.
func proxyRedirectHosts(raw string) (map[string]bool, error) {
	hosts := make(map[string]bool)
	// ProxyConfig.setRedirectedToHosts uses StringUtils.trim (characters <= 32).
	// Unicode whitespace must remain invalid rather than widening a host grant.
	raw = strings.TrimFunc(raw, func(c rune) bool { return c <= ' ' })
	for _, token := range strings.FieldsFunc(raw, func(c rune) bool {
		return c == '\n' || c == ' ' || c == ',' || c == ';'
	}) {
		host := proxyRedirectHost(token)
		if host == "" {
			return nil, remote.ErrConfiguration
		}
		hosts[host] = true
	}
	return hosts, nil
}

func proxyRedirectHost(host string) string {
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

func (s *sourceSecurity) redirectedProxyAuth(req *http.Request) bool {
	if s.proxy == nil || s.proxy.User == nil || req.Response == nil ||
		req.Header.Get("Authorization") != "" || req.Context().Value(noCrossOriginAuthKey{}) == true {
		return false
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	if req.Body != nil && req.Body != http.NoBody {
		return false
	}
	// The actual inherited jfrog-http-client 13.21.0 builder registers each
	// host at the configured proxy port, ANY_REALM. Its older static helper's
	// ANY_PORT differs. Neither registration grants destination reachability.
	return s.proxyRedirectHosts[proxyRedirectHost(req.URL.Hostname())] && effectivePort(req.URL) == s.proxy.Port()
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return strings.TrimLeft(port, "0")
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func basicProxyRedirectChallenge(values []string) bool {
	challenges, err := registryChallenges(values)
	if err != nil {
		return false
	}
	for _, challenge := range challenges {
		if challenge.scheme == "basic" {
			_, hasRealm := challenge.values["realm"]
			return hasRealm
		}
	}
	return false
}
