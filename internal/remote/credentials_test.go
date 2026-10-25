package remote

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

func resolverFor(t *testing.T, st store.Store, opts ResolverOptions) *Resolver {
	t.Helper()
	key, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewResolver(st, key, opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func saveRepo(t *testing.T, st store.Store, c repo.Config) {
	t.Helper()
	if err := st.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func resolveFor(t *testing.T, r *Resolver, c repo.Config) Options {
	t.Helper()
	opts, err := r.Options(t.Context(), repo.Masked(c))
	if err != nil {
		t.Fatal(err)
	}
	return opts
}

func getFor(t *testing.T, c *Client, path string) *http.Response {
	t.Helper()
	res, err := c.Do(t.Context(), Request{Method: http.MethodGet, Path: path, Header: http.Header{
		"Authorization": {
			"Bearer caller-secret",
		},
		"X-Jfrog-Art-Api":     {"caller-key"},
		"Cookie":              {"caller=cookie"},
		"Proxy-Authorization": {"caller-proxy"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func rootsFor(servers ...*fakeupstream.Server) []byte {
	var b []byte
	for _, s := range servers {
		b = append(
			b,
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})...)
	}
	return b
}

func putProxy(
	t *testing.T,
	st store.Store,
	key string,
	p wire.ProxyConfiguration,
	password string,
) {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Configs().
		Put(t.Context(), types.ConfigDocument{Kind: types.ConfigProxy, Key: key, Enabled: true, Document: b, Secrets: map[string]string{"password": password}}); err != nil {
		t.Fatal(err)
	}
}

func proxyConfig(t *testing.T, raw string) wire.ProxyConfiguration {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return wire.ProxyConfiguration{
		Host:     u.Scheme + "://" + u.Hostname(),
		Port:     p,
		Username: "proxy-user",
	}
}

func TestAccResolverSealedCredentialsAndRotation(t *testing.T) {
	key, _ := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	st, err := sqlite.Open(
		t.Context(),
		sqlite.Memory,
		sqlite.Options{Options: sqlcommon.Options{Keyring: key}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, token := range []bool{false, true} {
		t.Run(strconv.FormatBool(token), func(t *testing.T) {
			auth := fakeupstream.Auth{Username: "upstream-user", Password: "upstream-password"}
			if token {
				auth = fakeupstream.Auth{Token: "upstream-password"}
			}
			upstream := fakeupstream.New(t, fakeupstream.Options{Auth: auth})
			upstream.Set("/item", fakeupstream.Resource{Body: []byte("ok")})
			proxy := fakeupstream.NewProxy(
				t,
				fakeupstream.Auth{Username: "proxy-user", Password: "sealed-proxy-secret"},
				upstream,
			)
			proxyKey := "sealed-proxy-" + strconv.FormatBool(token)
			putProxy(t, st, proxyKey, proxyConfig(t, proxy.URL), "sealed-proxy-secret")
			c := repo.Config{
				Key:                       "credentials-" + strconv.FormatBool(token),
				Class:                     repo.Remote,
				PackageType:               repo.Generic,
				URL:                       upstream.URL,
				Username:                  "upstream-user",
				Password:                  "upstream-password",
				EnableTokenAuthentication: ptr(token),
				CustomHTTPHeaders: []repo.CustomHTTPHeader{
					{Name: "X-Custom-Secret", Value: "header-secret", Sensitive: true},
				},
			}
			c.Proxy = proxyKey
			saveRepo(t, st, c)
			resolver := resolverFor(t, st, ResolverOptions{})
			opts := resolveFor(t, resolver, c)
			pool := &Pool{}
			t.Cleanup(pool.Close)
			client, err := pool.Client(repo.Masked(c), opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := body(t, getFor(t, client, "item")); got != "ok" {
				t.Fatal(got)
			}
			recorded := upstream.Requests()[0]
			if !recorded.Authorized ||
				!recorded.HeaderMatches("X-Custom-Secret", "header-secret") ||
				recorded.Header.Get("Cookie") != "" ||
				recorded.Header.Get("X-Jfrog-Art-Api") != "" ||
				recorded.Header.Get("Proxy-Authorization") != "" {
				t.Fatal("credential boundary failed")
			}
			again := resolveFor(t, resolverFor(t, st, ResolverOptions{}), c)
			if opts.Revision != again.Revision || len(opts.Revision) != 64 {
				t.Fatal("revision must survive resolver restart")
			}
			// Policy changes rebuild the client, but must preserve immutable cache identity.
			c.RetrievalCachePeriodSecs = ptr(123)
			c.Offline = ptr(true)
			if err = st.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			if resolveFor(t, resolver, c).Revision != opts.Revision {
				t.Fatal("policy rotated source identity")
			}
			c.Password = "revoked-password"
			if err = st.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			rotated := resolveFor(t, resolver, c)
			if rotated.Revision == opts.Revision {
				t.Fatal("credential rotation retained identity")
			}
			fresh, err := pool.Client(repo.Masked(c), rotated)
			if err != nil {
				t.Fatal(err)
			}
			if fresh == client {
				t.Fatal("rotation reused old client")
			}
			res := getFor(t, fresh, "item")
			if res.StatusCode != 401 {
				t.Fatal(res.StatusCode)
			}
			body(t, res)
			c.CustomHTTPHeaders[0].Value = "changed-header"
			st.Repos().Update(t.Context(), c)
			if resolveFor(t, resolver, c).Revision == rotated.Revision {
				t.Fatal("sensitive header rotation retained identity")
			}
		})
	}
}

func TestAccResolverRedirectCredentialsAndMetadata(t *testing.T) {
	target := fakeupstream.New(
		t,
		fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12}},
	)
	target.Set("/target", fakeupstream.Resource{Body: []byte("target")})
	source := fakeupstream.New(
		t,
		fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12}},
	)
	source.Set(
		"/redirect",
		fakeupstream.Resource{Redirect: target.URL + "/target?signature=signed-secret"},
	)
	source.Set("/same", fakeupstream.Resource{Redirect: source.URL + "/target"})
	source.Set("/target", fakeupstream.Resource{Body: []byte("source")})
	st := inmem.New()
	c := repo.Config{
		Key:         "redirect",
		Class:       repo.Remote,
		URL:         source.URL,
		Username:    "repo-user",
		Password:    "repo-password",
		QueryParams: "must=not-propagate",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Custom-Secret", Value: "header-secret", Sensitive: true},
			{Name: "Accept", Value: "configured-accept"},
		},
	}
	saveRepo(t, st, c)
	for _, allow := range []bool{false, true} {
		t.Run(strconv.FormatBool(allow), func(t *testing.T) {
			c.AllowAnyHostAuth = ptr(allow)
			st.Repos().Update(t.Context(), c)
			r := resolverFor(
				t,
				st,
				ResolverOptions{
					RootCAsPEM:            rootsFor(source, target),
					AllowedPrivateOrigins: []string{target.URL},
				},
			)
			client := clientFor(t, c, resolveFor(t, r, c))
			if body(t, getFor(t, client, "redirect")) != "target" {
				t.Fatal("redirect failed")
			}
			records := target.Requests()
			last := records[len(records)-1]
			if last.HeaderMatches(
				"Authorization",
				"Basic cmVwby11c2VyOnJlcG8tcGFzc3dvcmQ=",
			) != allow ||
				last.HeaderMatches("X-Custom-Secret", "header-secret") != allow ||
				last.HeaderMatches("Accept", "configured-accept") != allow {
				t.Fatal("cross-origin credentials violated setting")
			}
			if last.Header.Get("Cookie") != "" {
				t.Fatal("caller cookie leaked")
			}
			if body(t, getFor(t, client, "same")) != "source" {
				t.Fatal("same-origin redirect failed")
			}
			records = source.Requests()
			if !records[len(records)-1].HeaderMatches("X-Custom-Secret", "header-secret") {
				t.Fatal("same-origin credential lost")
			}
			res, err := client.Do(
				t.Context(),
				Request{Method: "GET", ExternalURL: target.URL + "/target?signature=signed-secret"},
			)
			if err != nil {
				t.Fatal(err)
			}
			body(t, res)
			if res.Request.URL.RawQuery != "signature=signed-secret" {
				t.Fatal("absolute URL query changed")
			}
		})
	}
	// An origin encountered in metadata is not a private-network grant.
	restricted := resolverFor(t, st, ResolverOptions{RootCAsPEM: rootsFor(source, target)})
	client := clientFor(t, c, resolveFor(t, restricted, c))
	before := target.Count("", "")
	for _, request := range []Request{{Method: "GET", Path: "redirect"}, {Method: "GET", ExternalURL: target.URL + "/target?secret=secret"}, {Method: "GET", ExternalURL: "http://169.254.169.254/latest/api/token"}, {Method: "GET", ExternalURL: source.URL + "/target", Path: "mixed"}, {Method: "GET", ExternalURL: "https://user:secret@example.com"}} {
		res, err := client.Do(t.Context(), request)
		if res != nil {
			res.Body.Close()
		}
		if !errors.Is(err, ErrSecurity) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("denial = %v", err)
		}
	}
	if target.Count("", "") != before {
		t.Fatal("blocked target was contacted")
	}
	plain := fakeupstream.New(t, fakeupstream.Options{})
	source.Set("/downgrade", fakeupstream.Resource{Redirect: plain.URL + "/target"})
	if _, err := client.Do(
		t.Context(),
		Request{Method: "GET", Path: "downgrade"},
	); !errors.Is(
		err,
		ErrRedirect,
	) {
		t.Fatal(err)
	}
	if plain.Count("", "") != 0 {
		t.Fatal("downgrade reached target")
	}
}

func TestAccResolverCookiesArePerRepositoryAndRevision(t *testing.T) {
	upstream := fakeupstream.New(t, fakeupstream.Options{})
	upstream.Set(
		"/set",
		fakeupstream.Resource{
			Header: http.Header{"Set-Cookie": {"session=upstream-secret; Path=/"}},
			Body:   []byte("ok"),
		},
	)
	upstream.Set("/read", fakeupstream.Resource{Body: []byte("ok")})
	st := inmem.New()
	resolver := resolverFor(t, st, ResolverOptions{})
	pool := &Pool{}
	t.Cleanup(pool.Close)
	a := repo.Config{
		Key:                    "a",
		Class:                  repo.Remote,
		URL:                    upstream.URL,
		EnableCookieManagement: ptr(true),
	}
	b := a
	b.Key = "b"
	disabled := a
	disabled.Key = "disabled"
	disabled.EnableCookieManagement = ptr(false)
	for _, c := range []repo.Config{a, b, disabled} {
		saveRepo(t, st, c)
	}
	ca, _ := pool.Client(a, resolveFor(t, resolver, a))
	cb, _ := pool.Client(b, resolveFor(t, resolver, b))
	cd, _ := pool.Client(disabled, resolveFor(t, resolver, disabled))
	body(t, getFor(t, ca, "set"))
	body(t, getFor(t, ca, "read"))
	records := upstream.Requests()
	if !records[len(records)-1].HeaderMatches("Cookie", "session=upstream-secret") {
		t.Fatal("cookie not retained")
	}
	for _, c := range []*Client{cb, cd} {
		body(t, getFor(t, c, "read"))
		records = upstream.Requests()
		if records[len(records)-1].Header.Get("Cookie") != "" {
			t.Fatal("cookie leaked between repositories")
		}
	}
	body(t, getFor(t, cd, "set"))
	body(t, getFor(t, cd, "read"))
	records = upstream.Requests()
	if records[len(records)-1].Header.Get("Cookie") != "" {
		t.Fatal("disabled jar retained cookie")
	}
	a.Password = "new-secret"
	st.Repos().Update(t.Context(), a)
	fresh, _ := pool.Client(a, resolveFor(t, resolver, a))
	body(t, getFor(t, fresh, "read"))
	records = upstream.Requests()
	if records[len(records)-1].Header.Get("Cookie") != "" {
		t.Fatal("rotated identity retained cookie")
	}
}

func storeIdentity(t *testing.T, st store.Store, alias string, cert tls.Certificate) {
	t.Helper()
	var certPEM []byte
	for _, der := range cert.Certificate {
		certPEM = append(
			certPEM,
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Keys().
		PutCertificate(t.Context(), types.Certificate{Alias: alias, PEM: string(certPEM), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}); err != nil {
		t.Fatal(err)
	}
}

func TestAccResolverTLSClientIdentityAndRotation(t *testing.T) {
	identity, ca := fakeupstream.ClientIdentity(t)
	upstream := fakeupstream.New(
		t,
		fakeupstream.Options{
			TLS: &tls.Config{
				MinVersion: tls.VersionTLS12,
				ClientAuth: tls.RequireAndVerifyClientCert,
				ClientCAs:  ca,
			},
		},
	)
	upstream.Set("/item", fakeupstream.Resource{Body: []byte("mutual")})
	key, _ := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	st, err := sqlite.Open(
		t.Context(),
		sqlite.Memory,
		sqlite.Options{Options: sqlcommon.Options{Keyring: key}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	storeIdentity(t, st, "client", identity)
	c := repo.Config{
		Key:                  "mutual",
		Class:                repo.Remote,
		URL:                  upstream.URL,
		ClientTLSCertificate: "client",
	}
	saveRepo(t, st, c)
	r := resolverFor(t, st, ResolverOptions{RootCAsPEM: rootsFor(upstream)})
	opts := resolveFor(t, r, c)
	client := clientFor(t, c, opts)
	if body(t, getFor(t, client, "item")) != "mutual" ||
		!upstream.Requests()[0].VerifiedClientCertificate {
		t.Fatal("mTLS failed")
	}
	untrusted := clientFor(t, c, resolveFor(t, resolverFor(t, st, ResolverOptions{}), c))
	if _, err := untrusted.Do(
		t.Context(),
		Request{Method: "GET", Path: "item"},
	); !errors.Is(
		err,
		ErrSecurity,
	) {
		t.Fatal(err)
	}
	other, _ := fakeupstream.ClientIdentity(t)
	storeIdentity(t, st, "client", other)
	rotated := resolveFor(t, r, c)
	if rotated.Revision == opts.Revision {
		t.Fatal("certificate rotation retained identity")
	}
	rejected := clientFor(t, c, rotated)
	if _, err := rejected.Do(t.Context(), Request{Method: "GET", Path: "item"}); err == nil {
		t.Fatal("untrusted client identity accepted")
	}
	// A redirect destination requesting a client certificate cannot steal it by default.
	target := fakeupstream.New(
		t,
		fakeupstream.Options{
			TLS: &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequestClientCert},
		},
	)
	target.Set("/target", fakeupstream.Resource{Body: []byte("target")})
	upstream.Set("/redirect", fakeupstream.Resource{Redirect: target.URL + "/target"})
	storeIdentity(t, st, "client", identity)
	for _, allow := range []bool{false, true} {
		c.AllowAnyHostAuth = ptr(allow)
		st.Repos().Update(t.Context(), c)
		r = resolverFor(
			t,
			st,
			ResolverOptions{
				RootCAsPEM:            rootsFor(upstream, target),
				AllowedPrivateOrigins: []string{target.URL},
			},
		)
		cl := clientFor(t, c, resolveFor(t, r, c))
		body(t, getFor(t, cl, "redirect"))
		secure := cl.http.Transport.(*secureTransport)
		targetURL, _ := url.Parse(target.URL)
		certs := secure.forAuthority(targetURL, nil, false).TLSClientConfig.Certificates
		if (len(certs) > 0) != allow {
			t.Fatal("client certificate crossed origin unexpectedly")
		}
		if len(secure.forAuthority(targetURL, nil, true).TLSClientConfig.Certificates) != 0 {
			t.Fatal("format-selected external child received a client certificate")
		}
	}
}

func TestAccResolverProxyAndDNSPinning(t *testing.T) {
	for _, tlsOrigin := range []bool{false, true} {
		t.Run(strconv.FormatBool(tlsOrigin), func(t *testing.T) {
			opts := fakeupstream.Options{
				Auth: fakeupstream.Auth{Username: "source-user", Password: "source-pass"},
			}
			if tlsOrigin {
				opts.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
			}
			upstream := fakeupstream.New(t, opts)
			upstream.Set("/item", fakeupstream.Resource{Body: []byte("proxied")})
			proxy := fakeupstream.NewProxy(
				t,
				fakeupstream.Auth{Username: "proxy-user", Password: "proxy-pass"},
				upstream,
			)
			st := inmem.New()
			p := proxyConfig(t, proxy.URL)
			p.PlatformDefault = true
			putProxy(t, st, "proxy", p, "proxy-pass")
			c := repo.Config{
				Key:      "proxy",
				Class:    repo.Remote,
				URL:      upstream.URL,
				Username: "source-user",
				Password: "source-pass",
			}
			// A logical DNS name must never be resolved again by the forward proxy.
			u, _ := url.Parse(upstream.URL)
			host := "registry.private"
			if tlsOrigin {
				names := upstream.Certificate().DNSNames
				if len(names) == 0 {
					t.Fatal("TLS fixture needs a DNS identity")
				}
				host = names[0]
			}
			u.Host = net.JoinHostPort(host, u.Port())
			c.URL = u.String()
			saveRepo(t, st, c)
			resolverOpts := ResolverOptions{
				LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
				},
			}
			if tlsOrigin {
				resolverOpts.RootCAsPEM = rootsFor(upstream)
			}
			r := resolverFor(t, st, resolverOpts)
			resolved := resolveFor(t, r, c)
			client := clientFor(t, c, resolved)
			if body(t, getFor(t, client, "item")) != "proxied" {
				t.Fatal("proxy failed")
			}
			if !proxy.Requests()[0].Authorized || !upstream.Requests()[0].Authorized ||
				upstream.Requests()[0].Header.Get("Proxy-Authorization") != "" {
				t.Fatal("proxy auth isolation failed")
			}
			if upstream.Requests()[0].Host != u.Host {
				t.Fatal("logical Host lost")
			}
			putProxy(t, st, "proxy", p, "changed-proxy-secret")
			if resolveFor(t, r, c).Revision == resolved.Revision {
				t.Fatal("proxy rotation retained identity")
			}
			c.DisableProxy = ptr(true)
			st.Repos().Update(t.Context(), c)
			direct := clientFor(t, c, resolveFor(t, r, c))
			before := proxy.Count("", "")
			body(t, getFor(t, direct, "item"))
			if proxy.Count("", "") != before {
				t.Fatal("disableProxy still used proxy")
			}
		})
	}
}

func TestAccResolverConfigurationFailures(t *testing.T) {
	st := inmem.New()
	key, _ := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	for _, tc := range []struct {
		st   store.Store
		key  *crypt.Keyring
		opts ResolverOptions
	}{{nil, key, ResolverOptions{}}, {st, nil, ResolverOptions{}}, {st, key, ResolverOptions{RootCAsPEM: []byte("not a certificate")}}, {st, key, ResolverOptions{AllowedPrivateOrigins: []string{"ftp://example.com"}}}} {
		if _, err := NewResolver(tc.st, tc.key, tc.opts); err == nil {
			t.Fatal("invalid resolver accepted")
		}
	}
	c := repo.Config{Key: "config", Class: repo.Remote, URL: "https://example.com"}
	saveRepo(t, st, c)
	r := resolverFor(t, st, ResolverOptions{})
	missing := c
	missing.Key = "missing"
	if _, err := r.Options(t.Context(), missing); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	stale := c
	stale.URL = "https://stale.example"
	if _, err := r.Options(t.Context(), stale); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*repo.Config)
		want   error
	}{
		{"local", func(c *repo.Config) { c.Class = repo.Local }, ErrConfiguration},
		{"bad-url", func(c *repo.Config) { c.URL = "https://secret:secret@example.com" }, ErrSecurity},
		{"missing-cert", func(c *repo.Config) { c.ClientTLSCertificate = "missing" }, ErrConfiguration},
		{"bad-cert", func(c *repo.Config) { c.ClientTLSCertificate = "broken" }, ErrSecurity},
		{"missing-proxy", func(c *repo.Config) { c.Proxy = "missing" }, ErrConfiguration},
		{"bad-header-name", func(c *repo.Config) {
			c.CustomHTTPHeaders = []repo.CustomHTTPHeader{{Name: "bad name", Value: "value"}}
		}, ErrConfiguration},
		{"bad-header-value", func(c *repo.Config) {
			c.CustomHTTPHeaders = []repo.CustomHTTPHeader{{Name: "X-Secret", Value: "secret\r\nInjected: true"}}
		}, ErrConfiguration},
		{"reserved-header", func(c *repo.Config) {
			c.CustomHTTPHeaders = []repo.CustomHTTPHeader{{Name: "Proxy-Authorization", Value: "secret"}}
		}, ErrConfiguration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st.Keys().
				PutCertificate(t.Context(), types.Certificate{Alias: "broken", PEM: "broken", PrivateKey: "secret"})
			changed := c
			tc.change(&changed)
			st.Repos().Update(t.Context(), changed)
			_, err := r.Options(t.Context(), changed)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
			st.Repos().Update(t.Context(), c)
		})
	}
	if !errors.Is(configurationError(context.Canceled), context.Canceled) ||
		!errors.Is(configurationError(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("cancellation lost")
	}
}

func TestAccResolverProxyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		doc      string
		enabled  bool
		explicit bool
		want     bool
	}{
		{"explicit", `{"host":"localhost","port":8080}`, true, true, true},
		{"ipv6", `{"host":"http://[::1]","port":8080}`, true, true, true},
		{"disabled", `{"host":"localhost","port":8080}`, false, true, false},
		{"bad-json", `{`, true, true, false},
		{"bad-port", `{"host":"localhost","port":65536}`, true, true, false},
		{"ntlm", `{"host":"localhost","port":8080,"domain":"DOMAIN"}`, true, true, false},
		{"nthost", `{"host":"localhost","port":8080,"ntHost":"HOST"}`, true, true, false},
		{"redirects", `{"host":"localhost","port":8080,"redirectedToHosts":"*"}`, true, true, false},
		{"userinfo", `{"host":"http://user:secret@localhost","port":8080}`, true, true, false},
		{"path", `{"host":"http://localhost/path","port":8080}`, true, true, false},
		{"query", `{"host":"http://localhost?secret=1","port":8080}`, true, true, false},
		{"embedded-port", `{"host":"http://localhost:8080","port":8080}`, true, true, false},
		{"default", `{"host":"localhost","port":8080,"platformDefault":true,"services":["jfrt"]}`, true, false, true},
		{"bad-default", `{`, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := inmem.New()
			st.Configs().
				Put(t.Context(), types.ConfigDocument{Kind: types.ConfigProxy, Key: "proxy", Enabled: tc.enabled, Document: []byte(tc.doc)})
			c := repo.Config{Key: "config", Class: repo.Remote, URL: "https://example.com"}
			if tc.explicit {
				c.Proxy = "proxy"
			}
			saveRepo(t, st, c)
			r := resolverFor(t, st, ResolverOptions{})
			opts, err := r.Options(t.Context(), c)
			if tc.want {
				if err != nil || opts.ProxyURL == nil {
					t.Fatalf("proxy = %v, %v", opts.ProxyURL, err)
				}
			} else if !errors.Is(err, ErrConfiguration) {
				t.Fatal(err)
			}
		})
	}
	st := inmem.New()
	c := repo.Config{Key: "config", Class: repo.Remote, URL: "https://example.com"}
	saveRepo(t, st, c)
	r := resolverFor(t, st, ResolverOptions{})
	p := wire.ProxyConfiguration{
		Host:            "localhost",
		Port:            8080,
		PlatformDefault: true,
		Services:        []string{"xray"},
	}
	putProxy(t, st, "xray", p, "")
	if resolveFor(t, r, c).ProxyURL != nil {
		t.Fatal("used another service's default proxy")
	}
	p.Services = nil
	putProxy(t, st, "one", p, "")
	putProxy(t, st, "two", p, "")
	if _, err := r.Options(t.Context(), c); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
	c.DisableProxy = ptr(true)
	c.Proxy = "missing"
	st.Repos().Update(t.Context(), c)
	if resolveFor(t, r, c).ProxyURL != nil {
		t.Fatal("disableProxy did not ignore reference")
	}
}

// tlsProxyBridge changes only the proxy connection's TLS layer. The HTTP forward
// and CONNECT protocol still comes from the bounded fakeupstream proxy fixture.
func tlsProxyBridge(t *testing.T, proxy *fakeupstream.Server) (string, []byte, *atomic.Int32) {
	t.Helper()
	template := httptest.NewUnstartedServer(chi.NewRouter())
	template.StartTLS()
	config := template.TLS.Clone()
	config.ClientAuth = tls.RequestClientCert
	cert := template.Certificate()
	template.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(proxy.URL)
	var mu sync.Mutex
	active := map[net.Conn]bool{}
	var workers sync.WaitGroup
	peers := &atomic.Int32{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			client, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			active[client] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer client.Close()
				defer func() { mu.Lock(); delete(active, client); mu.Unlock() }()
				secure := client.(*tls.Conn)
				if secure.HandshakeContext(t.Context()) != nil {
					return
				}
				peers.Add(int32(len(secure.ConnectionState().PeerCertificates)))
				upstream, dialErr := (&net.Dialer{}).DialContext(t.Context(), "tcp", target.Host)
				if dialErr != nil {
					return
				}
				defer upstream.Close()
				copied := make(chan struct{})
				go func() { io.Copy(upstream, client); upstream.Close(); close(copied) }()
				io.Copy(client, upstream)
				client.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-done
		mu.Lock()
		for c := range active {
			c.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return "https://" + listener.Addr().
			String(),
		pem.EncodeToMemory(
			&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw},
		), peers
}

func TestAccResolverHTTPSProxy(t *testing.T) {
	for _, tlsOrigin := range []bool{false, true} {
		t.Run(strconv.FormatBool(tlsOrigin), func(t *testing.T) {
			opts := fakeupstream.Options{}
			if tlsOrigin {
				opts.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
			}
			target := fakeupstream.New(t, opts)
			target.Set("/item", fakeupstream.Resource{Body: []byte("tls proxy")})
			proxy := fakeupstream.NewProxy(
				t,
				fakeupstream.Auth{Username: "proxy-user", Password: "proxy-pass"},
				target,
			)
			proxyURL, trust, peers := tlsProxyBridge(t, proxy)
			st := inmem.New()
			identity, _ := fakeupstream.ClientIdentity(t)
			storeIdentity(t, st, "client", identity)
			p := proxyConfig(t, proxyURL)
			putProxy(t, st, "proxy", p, "proxy-pass")
			c := repo.Config{
				Key:                  "https-proxy",
				Class:                repo.Remote,
				URL:                  target.URL,
				Proxy:                "proxy",
				ClientTLSCertificate: "client",
			}
			saveRepo(t, st, c)
			if tlsOrigin {
				trust = append(trust, rootsFor(target)...)
			}
			resolverOptions := ResolverOptions{RootCAsPEM: trust}
			if !tlsOrigin {
				u, _ := url.Parse(c.URL)
				u.Host = net.JoinHostPort("registry.private", u.Port())
				c.URL = u.String()
				st.Repos().Update(t.Context(), c)
				resolverOptions.LookupIP = func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
				}
			}
			resolver := resolverFor(t, st, resolverOptions)
			client := clientFor(t, c, resolveFor(t, resolver, c))
			if body(t, getFor(t, client, "item")) != "tls proxy" {
				t.Fatal("HTTPS proxy failed")
			}
			if peers.Load() != 0 {
				t.Fatal("repository client certificate leaked to proxy")
			}
			if !proxy.Requests()[0].Authorized {
				t.Fatal("proxy credential not sent")
			}
			resolverOptions.RootCAsPEM = nil
			untrusted := clientFor(t, c, resolveFor(t, resolverFor(t, st, resolverOptions), c))
			if _, err := untrusted.Do(
				t.Context(),
				Request{Method: "GET", Path: "item"},
			); !errors.Is(
				err,
				ErrSecurity,
			) {
				t.Fatal(err)
			}
		})
	}
}

func TestAccResolverConfiguredEndpointAndDowngrade(t *testing.T) {
	target := fakeupstream.New(t, fakeupstream.Options{})
	target.Set("/item", fakeupstream.Resource{Body: []byte("endpoint")})
	st := inmem.New()
	c := repo.Config{
		Key:              "endpoint",
		Class:            repo.Remote,
		URL:              "http://source.invalid",
		PyPIRegistryURL:  target.URL,
		Password:         "repo-secret",
		AllowAnyHostAuth: ptr(true),
	}
	saveRepo(t, st, c)
	r := resolverFor(t, st, ResolverOptions{})
	client := clientFor(t, c, resolveFor(t, r, c))
	res, err := client.Do(t.Context(), Request{Method: "GET", ExternalURL: target.URL + "/item"})
	if err != nil {
		t.Fatal(err)
	}
	if body(t, res) != "endpoint" || target.Requests()[0].Header.Get("Authorization") != "" {
		t.Fatal("HTTP alternate endpoint received credentials")
	}
	c.URL = "https://source.invalid"
	st.Repos().Update(t.Context(), c)
	client = clientFor(t, c, resolveFor(t, r, c))
	if _, err = client.Do(
		t.Context(),
		Request{Method: "GET", ExternalURL: target.URL + "/item"},
	); !errors.Is(
		err,
		ErrSecurity,
	) {
		t.Fatal(err)
	}
	c.PyPIRegistryURL = "http://user:secret@example.com"
	st.Repos().Update(t.Context(), c)
	if _, err = r.Options(t.Context(), c); !errors.Is(err, ErrConfiguration) {
		t.Fatal(err)
	}
}

func TestAccResolverProxyDenialAndCancellation(t *testing.T) {
	target := fakeupstream.New(t, fakeupstream.Options{})
	target.Set("/item", fakeupstream.Resource{Body: []byte("target")})
	proxy := fakeupstream.NewProxy(
		t,
		fakeupstream.Auth{Username: "proxy-user", Password: "right"},
		target,
	)
	st := inmem.New()
	p := proxyConfig(t, proxy.URL)
	putProxy(t, st, "proxy", p, "wrong")
	u, _ := url.Parse(target.URL)
	u.Host = net.JoinHostPort("registry.private", u.Port())
	c := repo.Config{Key: "proxy-denial", Class: repo.Remote, URL: u.String(), Proxy: "proxy"}
	saveRepo(t, st, c)
	r := resolverFor(
		t,
		st,
		ResolverOptions{LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}},
	)
	client := clientFor(t, c, resolveFor(t, r, c))
	if _, err := client.Do(
		t.Context(),
		Request{Method: "GET", Path: "item"},
	); !errors.Is(
		err,
		ErrSecurity,
	) {
		t.Fatal(err)
	}
	if target.Count("", "") != 0 {
		t.Fatal("bad proxy credentials reached destination")
	}
	// Proxy response failures and cancellation must not retain a blocked tunnel.
	for _, response := range []string{"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n", "invalid response", ""} {
		left, right := net.Pipe()
		t.Cleanup(func() { left.Close(); right.Close() })
		base := &http.Transport{
			MaxResponseHeaderBytes: 1024,
			DialContext:            func(context.Context, string, string) (net.Conn, error) { return left, nil },
		}
		secure := &secureTransport{
			base:   base,
			source: &sourceSecurity{proxy: &url.URL{Scheme: "http", Host: "proxy.invalid"}},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			reader := bufio.NewReader(right)
			request, _ := http.ReadRequest(reader)
			if request != nil {
				request.Body.Close()
			}
			if response == "" {
				cancel()
			} else {
				io.WriteString(right, response)
				right.Close()
			}
		}()
		_, err := secure.tunnel(ctx, "tcp", "127.0.0.1:80", secure.source.proxy)
		if err == nil {
			t.Fatal("failed tunnel accepted")
		}
		<-done
	}
}

func TestAccResolverExplicitAuthorizationHeader(t *testing.T) {
	upstream := fakeupstream.New(
		t,
		fakeupstream.Options{Auth: fakeupstream.Auth{Token: "custom-token"}},
	)
	upstream.Set("/item", fakeupstream.Resource{Body: []byte("configured auth")})
	st := inmem.New()
	c := repo.Config{
		Key:   "header-auth",
		Class: repo.Remote,
		URL:   upstream.URL,
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "Authorization", Value: "Bearer custom-token", Sensitive: true},
		},
	}
	saveRepo(t, st, c)
	r := resolverFor(t, st, ResolverOptions{})
	client := clientFor(t, c, resolveFor(t, r, c))
	if body(t, getFor(t, client, "item")) != "configured auth" {
		t.Fatal("explicit Authorization header failed")
	}
	c.Username = "wrong-user"
	c.Password = "wrong-password"
	st.Repos().Update(t.Context(), c)
	client = clientFor(t, c, resolveFor(t, r, c))
	res := getFor(t, client, "item")
	if res.StatusCode != 401 {
		t.Fatal("explicit Basic credentials did not take precedence")
	}
	body(t, res)
}

func TestAccResolverRejectsIntermediateTLSDowngrade(t *testing.T) {
	source := fakeupstream.New(t, fakeupstream.Options{})
	secure := fakeupstream.New(
		t,
		fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12}},
	)
	target := fakeupstream.New(t, fakeupstream.Options{})
	source.Set("/start", fakeupstream.Resource{Redirect: secure.URL + "/redirect"})
	secure.Set("/redirect", fakeupstream.Resource{Redirect: target.URL + "/target"})
	target.Set("/target", fakeupstream.Resource{Body: []byte("unsafe")})
	st := inmem.New()
	c := repo.Config{
		Key:              "upgrade-downgrade",
		Class:            repo.Remote,
		URL:              source.URL,
		AllowAnyHostAuth: ptr(true),
	}
	saveRepo(t, st, c)
	r := resolverFor(
		t,
		st,
		ResolverOptions{
			RootCAsPEM:            rootsFor(secure),
			AllowedPrivateOrigins: []string{secure.URL, target.URL},
		},
	)
	client := clientFor(t, c, resolveFor(t, r, c))
	if _, err := client.Do(
		t.Context(),
		Request{Method: "GET", Path: "start"},
	); !errors.Is(
		err,
		ErrRedirect,
	) {
		t.Fatal(err)
	}
	if secure.Count("GET", "/redirect") != 1 || target.Count("", "") != 0 {
		t.Fatal("intermediate downgrade was not stopped")
	}
}
