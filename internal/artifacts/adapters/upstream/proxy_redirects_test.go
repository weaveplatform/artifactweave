package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
)

func redirectProxy(t *testing.T, target *fakeupstream.Server, others ...*fakeupstream.Server) *fakeupstream.Server {
	t.Helper()
	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp6", net.JoinHostPort("::1", u.Port()))
	if err != nil {
		t.Fatal(err)
	}
	return fakeupstream.NewProxyOn(t, listener,
		fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}, append(others, target)...)
}

func TestAccResolverProxyRedirectCredentials(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(strconv.FormatBool(secure), func(t *testing.T) {
			identity, ca := fakeupstream.ClientIdentity(t)
			options := fakeupstream.Options{Auth: fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}}
			if secure {
				options.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: ca}
			}
			target := fakeupstream.New(t, options)
			target.Set("/artifact", fakeupstream.Resource{Body: []byte("redirected artifact")})
			wrongPort := fakeupstream.New(t, options)
			wrongPort.Set("/artifact", fakeupstream.Resource{Body: []byte("wrong-port artifact")})
			sourceOptions := fakeupstream.Options{Auth: fakeupstream.Auth{Username: "repo-user", Password: "repo-secret"}}
			if secure {
				sourceOptions.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca}
			}
			source := fakeupstream.New(t, sourceOptions)
			source.Set("/start", fakeupstream.Resource{Redirect: target.URL + "/artifact?signature=private-query"})
			source.Set("/same", fakeupstream.Resource{Redirect: "/artifact"})
			source.Set("/artifact", fakeupstream.Resource{Body: []byte("same origin")})
			source.Set("/wrong-port", fakeupstream.Resource{Redirect: wrongPort.URL + "/artifact"})
			proxy := redirectProxy(t, target, source, wrongPort)
			st := inmem.New()
			storeIdentity(t, st, "redirect-client", identity)
			p := proxyConfig(t, proxy.URL)
			p.Host = "http://[::1]"
			p.RedirectedToHosts = "other.example; 127.0.0.1,example.org\n"
			putProxy(t, st, "redirect-proxy", p, "proxy-secret")
			c := repo.Config{
				Key: "redirect-source", Class: repo.Remote, URL: source.URL,
				Proxy: "redirect-proxy", Username: "repo-user", Password: "repo-secret",
				CustomHTTPHeaders: []repo.CustomHTTPHeader{{Name: "X-Repository-Secret", Value: "origin-only"}},
			}
			if secure {
				c.ClientTLSCertificate = "redirect-client"
			}
			saveRepo(t, st, c)
			resolverOptions := ResolverOptions{AllowedPrivateOrigins: []string{target.URL, wrongPort.URL}}
			if secure {
				resolverOptions.RootCAsPEM = rootsFor(source, target, wrongPort)
			}
			resolver := resolverFor(t, st, resolverOptions)
			client := clientFor(t, c, resolveFor(t, resolver, c))
			if got := body(t, getFor(t, client, "start")); got != "redirected artifact" {
				t.Fatal(got)
			}
			requests := target.Requests()
			if len(requests) != 2 || requests[0].Header.Get("Authorization") != "" ||
				!requests[1].Authorized || !requests[1].HeaderMatches("Authorization", "Basic cHJveHktdXNlcjpwcm94eS1zZWNyZXQ=") {
				t.Fatal("redirect target did not receive one challenged proxy credential attempt")
			}
			for _, record := range requests {
				if record.VerifiedClientCertificate {
					t.Fatal("proxy credential grant forwarded repository TLS identity")
				}
				for _, header := range []string{"Proxy-Authorization", "X-Repository-Secret", "Cookie"} {
					if record.Header.Get(header) != "" {
						t.Fatalf("target received %s", header)
					}
				}
				if record.Query.Get("signature") != fakeupstream.Redacted {
					t.Fatal("signed query was changed or exposed")
				}
			}
			if body(t, getFor(t, client, "same")) != "same origin" {
				t.Fatal("same-origin redirect failed")
			}
			for _, record := range source.Requests() {
				if !record.Authorized || !record.HeaderMatches("X-Repository-Secret", "origin-only") || record.Header.Get("Proxy-Authorization") != "" {
					t.Fatal("repository credential boundary changed")
				}
				if secure && !record.VerifiedClientCertificate {
					t.Fatal("origin lost repository client identity")
				}
			}
			for _, record := range proxy.Requests() {
				if !record.Authorized || record.Header.Get("Authorization") != "" && record.Method == http.MethodConnect {
					t.Fatal("proxy credential boundary changed")
				}
			}
			res := getFor(t, client, "wrong-port")
			if res.StatusCode != 401 {
				t.Fatalf("wrong port status: %d", res.StatusCode)
			}
			body(t, res)
			if records := wrongPort.Requests(); len(records) != 1 || records[0].Header.Get("Authorization") != "" || records[0].VerifiedClientCertificate {
				t.Fatal("host match granted credentials at the wrong port")
			}
			for _, tc := range []struct {
				name, hosts string
			}{
				{name: "unlisted", hosts: "unlisted.example"},
				{name: "suffix-is-not-host", hosts: "127.0.0.1.attacker.example"},
				{name: "removed"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before := target.Count("", "")
					p.RedirectedToHosts = tc.hosts
					putProxy(t, st, "redirect-proxy", p, "proxy-secret")
					fresh := clientFor(t, c, resolveFor(t, resolver, c))
					res := getFor(t, fresh, "start")
					if res.StatusCode != http.StatusUnauthorized {
						t.Fatalf("status %d", res.StatusCode)
					}
					body(t, res)
					if target.Count("", "") != before+1 || target.Requests()[before].Header.Get("Authorization") != "" {
						t.Fatal("unlisted target received credentials or retry")
					}
				})
			}
			p.RedirectedToHosts = "127.0.0.1"
			putProxy(t, st, "redirect-proxy", p, "proxy-secret")
			fresh := clientFor(t, c, resolveFor(t, resolver, c))
			for _, request := range []remote.Request{
				{Method: "GET", ExternalURL: target.URL + "/artifact"},
				{Method: "GET", Path: "start", NoCrossOriginAuth: true},
			} {
				before := target.Count("", "")
				res, err := fresh.Do(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				if res.StatusCode != 401 {
					t.Fatalf("status %d", res.StatusCode)
				}
				body(t, res)
				if target.Count("", "") != before+1 || target.Requests()[before].Header.Get("Authorization") != "" {
					t.Fatal("nonredirect or restricted request received proxy credentials")
				}
			}
			before := target.Count("", "")
			restricted := resolverFor(t, st, ResolverOptions{RootCAsPEM: resolverOptions.RootCAsPEM})
			denied := clientFor(t, c, resolveFor(t, restricted, c))
			if _, err := denied.Do(
				t.Context(),
				remote.Request{Method: "GET", Path: "start"},
			); !errors.Is(err, remote.ErrSecurity) ||
				strings.Contains(err.Error(), "private-query") {
				t.Fatalf("private redirect: %v", err)
			}
			if target.Count("", "") != before {
				t.Fatal("credential rule granted private reachability")
			}
			if secure {
				c.AllowAnyHostAuth = new(true)
				if err := st.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				client := clientFor(t, c, resolveFor(t, resolver, c))
				before := target.Count("", "")
				res := getFor(t, client, "start")
				if res.StatusCode != 401 {
					t.Fatal("repository auth was replaced")
				}
				body(t, res)
				records := target.Requests()
				if len(records) != before+1 || !records[before].HeaderMatches("Authorization", "Basic cmVwby11c2VyOnJlcG8tc2VjcmV0") || !records[before].VerifiedClientCertificate {
					t.Fatal("independent allowAnyHostAuth credential policy changed")
				}
			}
		})
	}
}

func TestUnitProxyRedirectHosts(t *testing.T) {
	for _, raw := range []string{"Example.COM. example.com,127.0.0.1;::1\nother.example", "", ", ; \n"} {
		hosts, err := proxyRedirectHosts(raw)
		if err != nil {
			t.Fatal(err)
		}
		if raw != "" && strings.Contains(raw, "Example") && (len(hosts) != 5 || !hosts["example.com."] || !hosts["example.com"] || !hosts["::1"]) {
			t.Fatal(hosts)
		}
	}
	for _, raw := range []string{"*", "*.example.com", "https://example.com", "example.com:8080", "user@example.com", "example.com/path", "a\tb", "a\rb", "a..b", "-a.example", "a-.example", "a_b", "[::1]", "bad%2eexample", "a?b", "a#b", "a\\b", strings.Repeat("a", 64), strings.Repeat("a.", 127) + "a"} {
		t.Run(strconv.Quote(raw), func(t *testing.T) {
			if _, err := proxyRedirectHosts(raw); !errors.Is(err, remote.ErrConfiguration) {
				t.Fatal("accepted malformed credential host")
			}
		})
	}
	for c := rune(0); c <= ' '; c++ {
		hosts, err := proxyRedirectHosts(string(c) + "example.com" + string(c))
		if err != nil || !hosts["example.com"] || len(hosts) != 1 {
			t.Errorf("canonical edge trim rejected U+%04X", c)
		}
	}
	for _, c := range []rune{'\u0085', '\u00a0', '\u1680', '\u2000', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000'} {
		for _, raw := range []string{string(c) + "example.com", "example.com" + string(c)} {
			t.Run(strconv.Quote(raw), func(t *testing.T) {
				if _, err := proxyRedirectHosts(raw); !errors.Is(err, remote.ErrConfiguration) {
					t.Fatal("Unicode whitespace became a credential grant")
				}
			})
		}
	}
}

func TestAccResolverProxyRedirectUnicodeEdges(t *testing.T) {
	target := fakeupstream.New(t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}})
	target.Set("/artifact", fakeupstream.Resource{Body: []byte("protected artifact")})
	source := fakeupstream.New(t, fakeupstream.Options{})
	u, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := redirectProxy(t, target, source)
	st := inmem.New()
	p := proxyConfig(t, proxy.URL)
	p.Host = "http://[::1]"
	c := repo.Config{Key: "unicode-redirect", Class: repo.Remote, URL: source.URL, Proxy: "redirect-proxy"}
	saveRepo(t, st, c)
	for _, tc := range []struct{ hosts, target string }{
		{"\u00a0127.0.0.1", "127.0.0.1"},
		{"127.0.0.1\u00a0", "127.0.0.1"},
		{"\u0130.example", "i.example"},
	} {
		t.Run(strconv.Quote(tc.hosts), func(t *testing.T) {
			logical := "http://" + net.JoinHostPort(tc.target, u.Port())
			source.Set("/start", fakeupstream.Resource{Redirect: logical + "/artifact"})
			resolver := resolverFor(t, st, ResolverOptions{
				AllowedPrivateOrigins: []string{logical},
				LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
				},
			})
			p.RedirectedToHosts = tc.hosts
			putProxy(t, st, "redirect-proxy", p, "proxy-secret")
			beforeTarget, beforeProxy := target.Count("", ""), proxy.Count("", "")
			opts, err := resolver.Options(t.Context(), c)
			if err == nil {
				// Exercise the resulting redirect so the regression proves the
				// credential effect, rather than only a parser mismatch.
				body(t, getFor(t, clientFor(t, c, opts), "start"))
			}
			for _, record := range target.Requests()[beforeTarget:] {
				if record.Header.Get("Authorization") != "" {
					t.Fatal("invalid Unicode host granted target proxy credentials")
				}
			}
			if !errors.Is(err, remote.ErrConfiguration) || target.Count("", "") != beforeTarget || proxy.Count("", "") != beforeProxy {
				t.Fatal("malformed Unicode host was not refused before network I/O")
			}
		})
	}
}

func TestUnitProxyRedirectUnicodeASCIIKeys(t *testing.T) {
	// The source UnicodeData/SpecialCasing audit finds Kelvin as the only
	// non-ASCII rune whose full root lowercase becomes an ASCII host label.
	// This also refuses broader Fold/IDNA aliases such as long s and ligatures.
	for r := rune(utf8.RuneSelf); r <= utf8.MaxRune; r++ {
		want := ""
		if r == '\u212a' {
			want = "k.example"
		}
		if got := proxyRedirectHost(string(r) + ".example"); got != want {
			t.Fatalf("U+%04X credential key %q, want %q", r, got, want)
		}
	}
	for _, host := range []string{"A\u0130.example", "I\u0307.example", "\u0130\u0307.example", "OS\u03a3.example"} {
		t.Run(strconv.Quote(host), func(t *testing.T) {
			if proxyRedirectHost(host) != "" {
				t.Fatalf("non-ASCII full lowercase became a host grant: %q", host)
			}
		})
	}
}

func TestAccResolverProxyRedirectLiteralHostKeys(t *testing.T) {
	for _, tc := range []struct {
		name, listed, hostname string
		allowed                bool
	}{
		{"listed-terminal-dot", "redirect.example.", "redirect.example", false},
		{"target-terminal-dot", "redirect.example", "redirect.example.", false},
		{"matching-terminal-dot", "REDIRECT.EXAMPLE.", "redirect.example.", true},
		{"case-only", "REDIRECT.EXAMPLE", "redirect.example", true},
		{"unicode-case-fold", "\u212a.example", "k.example", true},
		{"ipv4-mapped-alias", "::ffff:127.0.0.1", "127.0.0.1", false},
		{"expanded-mapped-alias", "0:0:0:0:0:ffff:7f00:1", "127.0.0.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := fakeupstream.New(t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}})
			target.Set("/artifact", fakeupstream.Resource{Body: []byte("protected artifact")})
			u, err := url.Parse(target.URL)
			if err != nil {
				t.Fatal(err)
			}
			logical := "http://" + net.JoinHostPort(tc.hostname, u.Port())
			source := fakeupstream.New(t, fakeupstream.Options{})
			source.Set("/start", fakeupstream.Resource{Redirect: logical + "/artifact"})
			proxy := redirectProxy(t, target, source)
			st := inmem.New()
			p := proxyConfig(t, proxy.URL)
			p.Host, p.RedirectedToHosts = "http://[::1]", tc.listed
			putProxy(t, st, "redirect-proxy", p, "proxy-secret")
			c := repo.Config{Key: "literal-redirect", Class: repo.Remote, URL: source.URL, Proxy: "redirect-proxy"}
			saveRepo(t, st, c)
			resolver := resolverFor(t, st, ResolverOptions{
				AllowedPrivateOrigins: []string{logical},
				LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
				},
			})
			res := getFor(t, clientFor(t, c, resolveFor(t, resolver, c)), "start")
			body(t, res)
			for _, record := range target.Requests() {
				if !tc.allowed && record.Header.Get("Authorization") != "" {
					t.Fatal("different literal host key received proxy credentials")
				}
			}
			wantStatus, wantRequests := http.StatusUnauthorized, 1
			if tc.allowed {
				wantStatus, wantRequests = http.StatusOK, 2
			}
			if res.StatusCode != wantStatus || target.Count("", "") != wantRequests {
				t.Fatalf("literal host match: status %d requests %d", res.StatusCode, target.Count("", ""))
			}
		})
	}
}

func TestUnitProxyRedirectCredentialScope(t *testing.T) {
	for _, tc := range []struct {
		name, target, proxy string
		want                bool
	}{
		{"http-default", "http://EXAMPLE.com/artifact", "http://proxy.example:80", true},
		{"http-explicit", "http://example.com:80/artifact", "http://proxy.example:80", true},
		{"https-default", "https://example.com/artifact", "http://proxy.example:443", true},
		{"https-explicit", "https://example.com:443/artifact", "http://proxy.example:443", true},
		{"http-wrong-port", "http://example.com:81/artifact", "http://proxy.example:80", false},
		{"https-wrong-port", "https://example.com:444/artifact", "http://proxy.example:443", false},
		{"wrong-host", "https://other.example:443/artifact", "http://proxy.example:443", false},
		{"subdomain", "https://child.example.com:443/artifact", "http://proxy.example:443", false},
		{"terminal-dot-is-distinct", "https://example.com.:443/artifact", "http://proxy.example:443", false},
		{"leading-zero", "https://example.com:00443/artifact", "http://proxy.example:443", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy, _ := url.Parse(tc.proxy)
			proxy.User = url.UserPassword("proxy-user", "proxy-secret")
			s := &sourceSecurity{proxy: proxy, proxyRedirectHosts: map[string]bool{"example.com": true}}
			req, err := http.NewRequestWithContext(t.Context(), "GET", tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Response = &http.Response{StatusCode: 302}
			if s.redirectedProxyAuth(req) != tc.want {
				t.Fatal("incorrect host/port credential scope")
			}
			for _, mutate := range []func(*http.Request){
				func(r *http.Request) { r.Header.Set("Authorization", "origin-credential") },
				func(r *http.Request) { r.Method = "POST" },
				func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("do not replay")) },
				func(r *http.Request) { r.Response = nil },
				func(r *http.Request) {
					*r = *r.WithContext(context.WithValue(r.Context(), noCrossOriginAuthKey{}, true))
				},
			} {
				restricted := req.Clone(req.Context())
				mutate(restricted)
				if s.redirectedProxyAuth(restricted) {
					t.Fatal("restricted request authorized")
				}
			}
		})
	}
}

func TestUnitProxyRedirectLiteralIPKeys(t *testing.T) {
	for _, tc := range []struct {
		listed, target string
		want           bool
	}{
		{"::1", "http://[::1]/artifact", true},
		{"2001:db8::a", "http://[2001:DB8::A]/artifact", true},
		{"2001:db8::1", "http://[2001:db8:0:0:0:0:0:1]/artifact", false},
		{"::ffff:127.0.0.1", "http://[::ffff:127.0.0.1]/artifact", true},
		{"::ffff:127.0.0.1", "http://127.0.0.1/artifact", false},
		{"::ffff:127.0.0.1", "http://[::ffff:7f00:1]/artifact", false},
	} {
		t.Run(tc.listed+"/"+tc.target, func(t *testing.T) {
			hosts, err := proxyRedirectHosts(tc.listed)
			if err != nil {
				t.Fatal(err)
			}
			proxy, _ := url.Parse("http://proxy.example:80")
			proxy.User = url.UserPassword("proxy-user", "proxy-secret")
			s := &sourceSecurity{proxy: proxy, proxyRedirectHosts: hosts}
			req, err := http.NewRequestWithContext(t.Context(), "GET", tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Response = &http.Response{StatusCode: 302}
			if s.redirectedProxyAuth(req) != tc.want {
				t.Fatal("IP spelling changed the literal credential scope")
			}
		})
	}
}

func TestAccProxyRedirectDNSAndChallengeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		rebind, unsupported, rejected bool
		count                         int
	}{
		{name: "dns-and-cookies", count: 2},
		{name: "dns-rebinding", rebind: true, count: 1},
		{name: "unsupported-challenge", unsupported: true, count: 1},
		{name: "one-retry", rejected: true, count: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}
			if tc.unsupported {
				auth = fakeupstream.Auth{Token: "unsupported-token"}
			}
			target := fakeupstream.New(t, fakeupstream.Options{Auth: auth})
			resource := fakeupstream.Resource{Body: []byte("dns-pinned artifact")}
			if tc.rejected {
				resource.Status = 401
				resource.Header = http.Header{"WWW-Authenticate": {`Basic realm="still denied"`}}
			}
			target.Set("/artifact", resource)
			u, err := url.Parse(target.URL)
			if err != nil {
				t.Fatal(err)
			}
			logical := "http://redirect.example:" + u.Port()
			origin := fakeupstream.New(t, fakeupstream.Options{})
			origin.Set("/start", fakeupstream.Resource{Redirect: logical + "/artifact", Header: http.Header{"Set-Cookie": {"origin-cookie=source-secret; Path=/"}}})
			proxy := redirectProxy(t, target, origin)
			st := inmem.New()
			p := proxyConfig(t, proxy.URL)
			p.Host = "http://[::1]"
			p.RedirectedToHosts = "REDIRECT.EXAMPLE"
			putProxy(t, st, "proxy", p, "proxy-secret")
			c := repo.Config{Key: "dns-redirect", Class: repo.Remote, URL: origin.URL, Proxy: "proxy", EnableCookieManagement: new(true)}
			saveRepo(t, st, c)
			var lookups atomic.Int32
			resolver := resolverFor(t, st, ResolverOptions{AllowedPrivateOrigins: []string{logical}, LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
				ip := "127.0.0.1"
				if lookups.Add(1) > 1 && tc.rebind {
					ip = "169.254.169.254"
				}
				return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
			}})
			client := clientFor(t, c, resolveFor(t, resolver, c))
			res, err := client.Do(t.Context(), remote.Request{Method: "GET", Path: "start"})
			if tc.rebind {
				if !errors.Is(err, remote.ErrSecurity) {
					t.Fatal("changed DNS accepted", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				status := 200
				if tc.unsupported || tc.rejected {
					status = 401
				}
				if res.StatusCode != status {
					t.Fatalf("status %d", res.StatusCode)
				}
				body(t, res)
			}
			if target.Count("", "") != tc.count {
				t.Fatal("unexpected target retry count")
			}
			for _, r := range target.Requests() {
				if r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
					t.Fatal("proxy credential grant crossed cookie/header boundary")
				}
			}
			for _, r := range proxy.Requests() {
				if r.Method == http.MethodConnect && r.Host != u.Host {
					t.Fatal("proxy received unchecked logical authority")
				}
			}
		})
	}
}

func TestUnitProxyRedirectBasicChallenge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		want   bool
	}{
		{"basic", []string{`Basic realm="redirect"`}, true},
		{"mixed", []string{`Digest realm="proxy", bAsIc realm="redirect,quoted"`}, true},
		{"multiple-headers", []string{`Bearer realm="other"`, `Basic realm="redirect", charset="UTF-8"`}, true},
		{"empty", nil, false},
		{"realm-required", []string{"Basic"}, false},
		{"unsupported", []string{`NTLM`}, false},
		{"malformed", []string{`Basic realm="unterminated`}, false},
		{"duplicate", []string{`Basic realm="one",realm="two"`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if basicProxyRedirectChallenge(tc.values) != tc.want {
				t.Fatal("incorrect challenge decision")
			}
		})
	}
}

func TestUnitProxyRedirectSourceRevisionsAndPools(t *testing.T) {
	st := inmem.New()
	p := wire.ProxyConfiguration{Host: "proxy.example", Port: 8080, Username: "proxy-user", RedirectedToHosts: "allowed.example"}
	putProxy(t, st, "proxy", p, "proxy-secret")
	c := repo.Config{Key: "first", Class: repo.Remote, URL: "https://origin.example", Proxy: "proxy", EnableCookieManagement: new(true)}
	saveRepo(t, st, c)
	resolver := resolverFor(t, st, ResolverOptions{})
	pool := &Pool{}
	t.Cleanup(pool.Close)
	opts := resolveFor(t, resolver, c)
	first, err := pool.Client(repo.Masked(c), opts)
	if err != nil {
		t.Fatal(err)
	}
	again, err := pool.Client(repo.Masked(c), resolveFor(t, resolver, c))
	if err != nil || again != first {
		t.Fatal("unchanged source did not reuse client", err)
	}
	other := c
	other.Key = "second"
	saveRepo(t, st, other)
	second, err := pool.Client(repo.Masked(other), resolveFor(t, resolver, other))
	if err != nil || first == second || first.http.Jar == second.http.Jar {
		t.Fatal("unrelated repositories shared authentication or cookie state", err)
	}
	for _, tc := range []struct{ name, hosts, password string }{
		{name: "changed-host", hosts: "other.example", password: "proxy-secret"},
		{name: "removed", password: "proxy-secret"},
		{name: "rotated-secret", hosts: "allowed.example", password: "new-proxy-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.RedirectedToHosts = tc.hosts
			putProxy(t, st, "proxy", p, tc.password)
			next := resolveFor(t, resolver, c)
			if next.Revision == opts.Revision {
				t.Fatal("proxy change retained source revision")
			}
			fresh, err := pool.Client(repo.Masked(c), next)
			if err != nil || fresh == first || fresh.http.Jar == first.http.Jar {
				t.Fatal("proxy change retained source authentication state", err)
			}
			opts, first = next, fresh
		})
	}
}
