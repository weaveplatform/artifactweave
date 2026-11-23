package upstream

import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
)

// proxyRedirectUsername is the Basic username a redirected host receives. With a
// proxy domain, jfrog-http-client registers NTCredentials for the listed hosts
// too, and Apache's BasicScheme sends NTUserPrincipal's name: the domain in
// Java's root-locale upper case, a backslash, then the username. Sending the
// bare username would authenticate a different account at the redirect host.
func (s *sourceSecurity) proxyRedirectUsername() string {
	username := s.proxy.User.Username()
	if s.proxyDomain == "" {
		return username
	}
	// Full casing matches String.toUpperCase (ß becomes SS); a fresh caser per
	// call because casers may be stateful.
	return cases.Upper(language.Und).String(s.proxyDomain) + `\` + username
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
	return s.proxyRedirectHosts[outbound.ProxyRedirectHost(req.URL.Hostname())] && effectivePort(req.URL) == s.proxy.Port()
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
