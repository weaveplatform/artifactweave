package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	sqlitedrv "github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccProxyNTLM(t *testing.T) {
	for _, secureOrigin := range []bool{false, true} {
		for _, secureProxy := range []bool{false, true} {
			t.Run(map[bool]string{false: "http", true: "https"}[secureOrigin]+"-"+
				map[bool]string{false: "http-proxy", true: "https-proxy"}[secureProxy], func(t *testing.T) {
				var originTLS, proxyTLS *tls.Config
				if secureOrigin {
					originTLS = &tls.Config{MinVersion: tls.VersionTLS12}
				}
				if secureProxy {
					proxyTLS = &tls.Config{MinVersion: tls.VersionTLS12}
				}
				origin := fakeupstream.New(t, fakeupstream.Options{
					TLS:  originTLS,
					Auth: fakeupstream.Auth{Username: "origin-user", Password: "origin-secret"},
				})
				origin.Set("/item", fakeupstream.Resource{Body: []byte("NTLM upstream bytes")})
				proxy := fakeupstream.NewNTLMProxy(t, fakeupstream.NTLMOptions{
					Username: "proxy-user", Password: "proxy-secret", Domain: "CORP", Workstation: "ARTIFACTWEAVE", TLS: proxyTLS,
				}, origin)
				t.Cleanup(func() {
					if t.Failed() {
						t.Logf("safe NTLM records: %+v", proxy.Negotiations())
					}
				})
				st := ntlmSQLStore(t, false)
				p := proxyConfig(t, proxy.URL)
				p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
				putProxy(t, st, "ntlm", p, "proxy-secret")
				c := repo.Config{
					Key: "ntlm", Class: repo.Remote, URL: origin.URL, Proxy: "ntlm",
					Username: "origin-user", Password: "origin-secret",
				}
				if !secureOrigin {
					u, _ := url.Parse(c.URL)
					u.Host = net.JoinHostPort("registry.private", u.Port())
					c.URL = u.String()
				}
				saveRepo(t, st, c)
				var trust []byte
				if secureOrigin {
					trust = append(trust, rootsFor(origin)...)
				}
				if secureProxy {
					trust = append(trust, rootsFor(proxy.Server)...)
				}
				resolver := resolverFor(t, st, ResolverOptions{RootCAsPEM: trust, LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
				}})
				client, err := NewClient(c, resolveFor(t, resolver, c))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(client.Close)
				for range 2 {
					if got := body(t, getFor(t, client, "item")); got != "NTLM upstream bytes" {
						t.Fatalf("unexpected upstream body %q", got)
					}
				}
				records := proxy.Negotiations()
				if len(records) != 3 || records[0].Message != 0 || records[1].Message != 1 ||
					records[2].Message != 3 || !records[2].Authorized ||
					records[0].Connection != records[2].Connection || records[1].Connection != records[2].Connection {
					t.Fatalf("expected one verified same-connection NTLM exchange: %+v", records)
				}
				for _, record := range records {
					if record.OriginAuthorization {
						t.Fatal("origin credentials reached proxy authentication")
					}
				}
				if records[1].Domain != "CORP" || records[1].Workstation != "ARTIFACTWEAVE" {
					t.Fatal("negotiate domain/workstation not verified")
				}
				if origin.Count(http.MethodGet, "/item") != 2 {
					t.Fatal("upstream bytes were not fetched twice through the reused tunnel")
				}
				for _, request := range origin.Requests() {
					if !request.Authorized || request.Header.Get("Proxy-Authorization") != "" {
						t.Fatal("proxy and origin credentials crossed")
					}
				}
			})
		}
	}
}

func ntlmSQLStore(t *testing.T, postgres bool) *appstoretest.SQLStore {
	t.Helper()
	key, err := crypt.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	var st *appstoretest.SQLStore
	if postgres {
		dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
		st, err = appstoretest.OpenPostgres(t.Context(), dsn, appstoretest.PostgresOptions{Options: appstoretest.Options{Keyring: key}})
	} else {
		st, err = appstoretest.OpenSQLite(t.Context(), sqlitedrv.Memory, appstoretest.SQLiteOptions{Options: appstoretest.Options{Keyring: key}})
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}

func ntlmFixtureOptions() fakeupstream.NTLMOptions {
	return fakeupstream.NTLMOptions{Username: "proxy-user", Password: "proxy-secret", Domain: "CORP", Workstation: "ARTIFACTWEAVE"}
}

func TestAccProxyNTLMFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fault    func(*fakeupstream.NTLMOptions)
		password string
		expected error
	}{
		{name: "wrong-password", password: "wrong-secret", expected: remote.ErrSecurity},
		{name: "wrong-account", fault: func(o *fakeupstream.NTLMOptions) { o.Username = "other-user" }, expected: remote.ErrSecurity},
		{name: "wrong-domain", fault: func(o *fakeupstream.NTLMOptions) { o.Domain = "OTHER" }, expected: remote.ErrSecurity},
		{name: "wrong-workstation", fault: func(o *fakeupstream.NTLMOptions) { o.Workstation = "OTHER" }, expected: remote.ErrSecurity},
		{name: "malformed-challenge", fault: func(o *fakeupstream.NTLMOptions) { o.Challenge = []byte("proxy-secret-invalid-packet") }, expected: remote.ErrSecurity},
		{name: "interrupted", fault: func(o *fakeupstream.NTLMOptions) { o.DropAfterNegotiate = true }, expected: remote.ErrSecurity},
		{name: "closed-challenge", fault: func(o *fakeupstream.NTLMOptions) { o.CloseAfterChallenge = true }, expected: remote.ErrSecurity},
		{name: "no-basic-fallback", fault: func(o *fakeupstream.NTLMOptions) { o.InitialAuthenticate = "Basic realm=fixture" }, expected: remote.ErrSecurity},
		{name: "quoted-scheme", fault: func(o *fakeupstream.NTLMOptions) { o.InitialAuthenticate = `Basic realm="other, NTLM, scheme"` }, expected: remote.ErrSecurity},
		{name: "invalid-base64", fault: func(o *fakeupstream.NTLMOptions) { o.InitialAuthenticate = "NTLM !!!" }, expected: remote.ErrSecurity},
		{name: "large-challenge-body", fault: func(o *fakeupstream.NTLMOptions) { o.ChallengeBody = bytes.Repeat([]byte("x"), 65<<10) }, expected: remote.ErrSecurity},
		{name: "framed-challenge-body", fault: func(o *fakeupstream.NTLMOptions) { o.ChallengeBody = []byte("proxy refusal body") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := fakeupstream.New(t, fakeupstream.Options{})
			origin.Set("/item", fakeupstream.Resource{Body: []byte("bytes")})
			options := ntlmFixtureOptions()
			options.Forward = true
			if tc.fault != nil {
				tc.fault(&options)
			}
			proxy := fakeupstream.NewNTLMProxy(t, options, origin)
			st := inmem.New()
			p := proxyConfig(t, proxy.URL)
			p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
			password := "proxy-secret"
			if tc.password != "" {
				password = tc.password
			}
			putProxy(t, st, "ntlm", p, password)
			c := repo.Config{Key: "ntlm", Class: repo.Remote, URL: origin.URL, Proxy: "ntlm", Username: "origin-user", Password: "origin-secret"}
			saveRepo(t, st, c)
			resolver := resolverFor(t, st, ResolverOptions{})
			client := clientFor(t, c, resolveFor(t, resolver, c))
			res, err := client.Do(t.Context(), remote.Request{Method: http.MethodGet, Path: "item"})
			if tc.expected == nil {
				if err != nil {
					t.Fatal(err)
				}
				if body(t, res) != "bytes" {
					t.Fatal("body differs")
				}
				return
			}
			if res != nil {
				_ = res.Body.Close()
				t.Fatal("failure returned an origin response")
			}
			if !errors.Is(err, tc.expected) {
				t.Fatalf("error %v, want %v", err, tc.expected)
			}
			for _, secret := range []string{"proxy-secret", "wrong-secret", "origin-secret", "NTLMSSP", "other-user"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("transport error leaked credentials")
				}
			}
			if origin.Count("", "") != 0 {
				t.Fatal("denied handshake reached origin")
			}
			for _, record := range proxy.Negotiations() {
				if record.Authorized {
					t.Fatal("failed handshake accepted")
				}
			}
		})
	}
}

func TestAccProxyNTLMRotation(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[postgres], func(t *testing.T) {
			st := ntlmSQLStore(t, postgres)
			origin := fakeupstream.New(t, fakeupstream.Options{})
			origin.Set("/item", fakeupstream.Resource{Body: []byte("sealed bytes")})
			proxy := fakeupstream.NewNTLMProxy(t, ntlmFixtureOptions(), origin)
			p := proxyConfig(t, proxy.URL)
			p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
			putProxy(t, st, "ntlm", p, "proxy-secret")
			var sealed []byte
			query := st.Dialect().Rebind("SELECT value FROM config_secrets WHERE kind = ? AND key = ? AND name = ?")
			if err := st.DB().QueryRowContext(t.Context(), query, configdoc.ConfigProxy, "ntlm", "password").Scan(&sealed); err != nil {
				t.Fatal(err)
			}
			if !crypt.IsSealed(sealed) || bytes.Contains(sealed, []byte("proxy-secret")) {
				t.Fatal("NTLM password was not sealed at rest")
			}
			u, _ := url.Parse(origin.URL)
			u.Host = net.JoinHostPort("registry.private", u.Port())
			c := repo.Config{Key: "ntlm", Class: repo.Remote, URL: u.String(), Proxy: "ntlm"}
			saveRepo(t, st, c)
			resolver := resolverFor(t, st, ResolverOptions{LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			}})
			pool := &Pool{}
			t.Cleanup(pool.Close)
			opts := resolveFor(t, resolver, c)
			first, err := pool.Client(c, opts)
			if err != nil {
				t.Fatal(err)
			}
			if body(t, getFor(t, first, "item")) != "sealed bytes" {
				t.Fatal("initial bytes differ")
			}
			connection := proxy.Negotiations()[2].Connection
			// A referenced sealed password rotates the client even while the repository row is unchanged.
			putProxy(t, st, "ntlm", p, "rotated-proxy-secret")
			changed := resolveFor(t, resolver, c)
			second, err := pool.Client(c, changed)
			if err != nil {
				t.Fatal(err)
			}
			if first == second || opts.Revision == changed.Revision {
				t.Fatal("password rotation reused authentication state")
			}
			select {
			case <-proxy.ConnectionClosed(connection):
			case <-time.After(3 * time.Second):
				t.Fatal("old authenticated idle tunnel remained open")
			}
			res, err := second.Do(t.Context(), remote.Request{Method: http.MethodGet, Path: "item"})
			if res != nil {
				_ = res.Body.Close()
			}
			if !errors.Is(err, remote.ErrSecurity) {
				t.Fatalf("stale proxy credentials still worked: %v", err)
			}
			options := ntlmFixtureOptions()
			options.Password = "rotated-proxy-secret"
			options.Domain = "NEWCORP"
			options.Workstation = "NEWNODE"
			replacement := fakeupstream.NewNTLMProxy(t, options, origin)
			p = proxyConfig(t, replacement.URL)
			p.Domain, p.NTHost = options.Domain, options.Workstation
			putProxy(t, st, "ntlm", p, options.Password)
			third, err := pool.Client(c, resolveFor(t, resolver, c))
			if err != nil {
				t.Fatal(err)
			}
			if third == second || body(t, getFor(t, third, "item")) != "sealed bytes" {
				t.Fatal("new proxy identity not active")
			}
			records := replacement.Negotiations()
			if len(records) != 3 || !records[2].Authorized || records[2].Domain != "NEWCORP" || records[2].Workstation != "NEWNODE" {
				t.Fatalf("new configuration was not used: %+v", records)
			}
			if origin.Count(http.MethodGet, "/item") != 2 {
				t.Fatal("failed rotated credentials reached origin")
			}
		})
	}
}

func TestAccProxyNTLMCancellation(t *testing.T) {
	barrier := &fakeupstream.Barrier{}
	t.Cleanup(barrier.Release)
	origin := fakeupstream.New(t, fakeupstream.Options{})
	options := ntlmFixtureOptions()
	options.BeforeChallenge = barrier
	options.Forward = true
	proxy := fakeupstream.NewNTLMProxy(t, options, origin)
	st := inmem.New()
	p := proxyConfig(t, proxy.URL)
	p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
	putProxy(t, st, "ntlm", p, "proxy-secret")
	zero := 0
	c := repo.Config{Key: "ntlm", Class: repo.Remote, URL: origin.URL, Proxy: "ntlm", SocketTimeoutMillis: &zero}
	saveRepo(t, st, c)
	client := clientFor(t, c, resolveFor(t, resolverFor(t, st, ResolverOptions{}), c))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		res, err := client.Do(ctx, remote.Request{Method: http.MethodGet, Path: "item"})
		if res != nil {
			_ = res.Body.Close()
		}
		done <- err
	}()
	<-barrier.Reached()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation left negotiation blocked")
	}
	barrier.Release()
	if origin.Count("", "") != 0 {
		t.Fatal("interrupted negotiation reached origin")
	}
}

func TestAccProxyNTLMLogicalHostAndSecurity(t *testing.T) {
	for _, secureOrigin := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "https-mtls"}[secureOrigin], func(t *testing.T) {
			identity, ca := fakeupstream.ClientIdentity(t)
			var originTLS *tls.Config
			if secureOrigin {
				originTLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca}
			}
			origin := fakeupstream.New(t, fakeupstream.Options{TLS: originTLS, Auth: fakeupstream.Auth{Username: "origin-user", Password: "origin-secret"}})
			origin.Set("/item", fakeupstream.Resource{Body: []byte("logical host bytes")})
			options := ntlmFixtureOptions()
			options.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequestClientCert, ClientCAs: ca}
			proxy := fakeupstream.NewNTLMProxy(t, options, origin)
			st := inmem.New()
			storeIdentity(t, st, "origin-client", identity)
			p := proxyConfig(t, proxy.URL)
			p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
			putProxy(t, st, "ntlm", p, "proxy-secret")
			physical, _ := url.Parse(origin.URL)
			logical := *physical
			host := "registry.private"
			if secureOrigin {
				host = origin.Certificate().DNSNames[0]
			}
			logical.Host = net.JoinHostPort(host, logical.Port())
			c := repo.Config{
				Key:                  "ntlm",
				Class:                repo.Remote,
				URL:                  logical.String(),
				Proxy:                "ntlm",
				Username:             "origin-user",
				Password:             "origin-secret",
				ClientTLSCertificate: "origin-client",
			}
			saveRepo(t, st, c)
			trust := rootsFor(proxy.Server)
			if secureOrigin {
				trust = append(trust, rootsFor(origin)...)
			}
			var rebound atomic.Bool
			resolverOpts := ResolverOptions{RootCAsPEM: trust, LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
				ip := "127.0.0.1"
				if rebound.Load() {
					ip = "169.254.169.254"
				}
				return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
			}}
			resolver := resolverFor(t, st, resolverOpts)
			client := clientFor(t, c, resolveFor(t, resolver, c))
			if body(t, getFor(t, client, "item")) != "logical host bytes" {
				t.Fatal("logical host download differs")
			}
			for _, record := range proxy.Requests() {
				if record.Host != physical.Host || record.VerifiedClientCertificate {
					t.Fatal("CONNECT pin or proxy TLS identity crossed")
				}
			}
			record := origin.Requests()[0]
			if record.Host != logical.Host || !record.Authorized || record.VerifiedClientCertificate != secureOrigin {
				t.Fatal("logical Host/SNI, source auth or mTLS lost")
			}
			for _, raw := range []string{"http://169.254.169.254/item", "http://untrusted.invalid/item"} {
				res, err := client.Do(t.Context(), remote.Request{Method: http.MethodGet, ExternalURL: raw})
				if res != nil {
					_ = res.Body.Close()
				}
				if !errors.Is(err, remote.ErrSecurity) {
					t.Fatalf("untrusted destination accepted: %v", err)
				}
			}
			rebound.Store(true)
			res, err := client.Do(t.Context(), remote.Request{Method: http.MethodGet, Path: "item"})
			if res != nil {
				_ = res.Body.Close()
			}
			if !errors.Is(err, remote.ErrSecurity) {
				t.Fatalf("changing DNS bypassed pinning: %v", err)
			}
			if len(proxy.Negotiations()) != 3 || origin.Count(http.MethodGet, "/item") != 1 {
				t.Fatal("denied/rebound destination reused authenticated authority")
			}
			rebound.Store(false)
			untrusted := resolverOpts
			untrusted.RootCAsPEM = nil
			denied := clientFor(t, c, resolveFor(t, resolverFor(t, st, untrusted), c))
			res, err = denied.Do(t.Context(), remote.Request{Method: http.MethodGet, Path: "item"})
			if res != nil {
				_ = res.Body.Close()
			}
			if !errors.Is(err, remote.ErrSecurity) {
				t.Fatalf("untrusted proxy TLS accepted: %v", err)
			}
		})
	}
}

func TestAccProxyNTLMForward(t *testing.T) {
	for _, secureProxy := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut} {
			t.Run(map[bool]string{false: "http-proxy", true: "https-proxy"}[secureProxy]+"-"+method, func(t *testing.T) {
				origin := fakeupstream.New(t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "origin-user", Password: "origin-secret"}})
				path := "/item"
				var payload []byte
				if method == http.MethodPost {
					path = "/objects/batch"
					payload = []byte(`{"operation":"download","objects":[]}`)
				}
				if method == http.MethodPut {
					payload = []byte("non-replayable Smart Remote statistics")
				}
				resource := fakeupstream.Resource{Method: method, Body: []byte("forwarded native bytes"), ExpectedBody: payload}
				origin.Set(path, resource)
				origin.Set("/reuse", fakeupstream.Resource{Body: []byte("reused")})
				options := ntlmFixtureOptions()
				options.Forward = true
				options.ForwardOnly = true
				if secureProxy {
					options.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
				}
				proxy := fakeupstream.NewNTLMProxy(t, options, origin)
				st := inmem.New()
				p := proxyConfig(t, proxy.URL)
				p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
				putProxy(t, st, "ntlm", p, "proxy-secret")
				c := repo.Config{Key: "ntlm", Class: repo.Remote, URL: origin.URL, Proxy: "ntlm", Username: "origin-user", Password: "origin-secret", PackageType: repo.GitLFS}
				saveRepo(t, st, c)
				resolverOpts := ResolverOptions{}
				if secureProxy {
					resolverOpts.RootCAsPEM = rootsFor(proxy.Server)
				}
				client := clientFor(t, c, resolveFor(t, resolverFor(t, st, resolverOpts), c))
				var response *http.Response
				var err error
				if method == http.MethodPut {
					request, e := http.NewRequestWithContext(t.Context(), method, origin.URL+path, bytes.NewReader(payload))
					if e != nil {
						t.Fatal(e)
					}
					request.GetBody = nil
					if err = client.Prepare(request); err != nil {
						t.Fatal(err)
					}
					response, err = client.Send(request)
				} else {
					r := remote.Request{Method: method, Path: strings.TrimPrefix(path, "/"), Body: payload}
					if method == http.MethodPost {
						r.Metadata = true
						r.Header = http.Header{"Accept": {"application/vnd.git-lfs+json"}}
					}
					response, err = client.Do(t.Context(), r)
				}
				if err != nil {
					t.Fatal(err)
				}
				status := response.StatusCode
				got := body(t, response)
				if status != http.StatusOK || method != http.MethodHead && got != "forwarded native bytes" {
					t.Fatalf("forwarded method/body changed: status%d", status)
				}
				if body(t, getFor(t, client, "reuse")) != "reused" {
					t.Fatal("authenticated forwarding reuse failed")
				}
				records := proxy.Negotiations()
				if len(records) < 4 || records[0].Message != 0 || records[1].Message != 1 || records[2].Message != 3 || !records[2].Authorized {
					t.Fatalf("standard HTTP NTLM exchange missing: %+v", records)
				}
				for _, record := range records {
					if record.Connection != records[0].Connection {
						t.Fatal("forward negotiation moved to another socket")
					}
				}
				sourceHeaders := &http.Request{Header: make(http.Header)}
				sourceHeaders.SetBasicAuth("origin-user", "origin-secret")
				for _, record := range proxy.Requests() {
					if !record.HeaderMatches("Authorization", sourceHeaders.Header.Get("Authorization")) ||
						record.HeaderMatches("Proxy-Authorization", sourceHeaders.Header.Get("Authorization")) {
						t.Fatal("forwarding substituted origin/proxy credentials")
					}
					if record.Method == http.MethodConnect {
						t.Fatal("literal-IP HTTP unexpectedly used CONNECT")
					}
				}
				if origin.Count(method, path) != 1 {
					t.Fatal("origin request was replayed or omitted")
				}
				for _, record := range origin.Requests() {
					if !record.Authorized || record.Header.Get("Proxy-Authorization") != "" {
						t.Fatal("origin and proxy credentials crossed")
					}
				}
			})
		}
	}
}

func TestAccProxyNTLMForwardRotation(t *testing.T) {
	st := ntlmSQLStore(t, false)
	origin := fakeupstream.New(t, fakeupstream.Options{})
	origin.Set("/item", fakeupstream.Resource{Body: []byte("forward rotation bytes")})
	options := ntlmFixtureOptions()
	options.Forward = true
	options.ForwardOnly = true
	proxy := fakeupstream.NewNTLMProxy(t, options, origin)
	p := proxyConfig(t, proxy.URL)
	p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
	putProxy(t, st, "ntlm", p, "proxy-secret")
	c := repo.Config{Key: "ntlm", Class: repo.Remote, URL: origin.URL, Proxy: "ntlm"}
	saveRepo(t, st, c)
	resolver := resolverFor(t, st, ResolverOptions{})
	pool := &Pool{}
	t.Cleanup(pool.Close)
	first, err := pool.Client(c, resolveFor(t, resolver, c))
	if err != nil {
		t.Fatal(err)
	}
	if body(t, getFor(t, first, "item")) != "forward rotation bytes" {
		t.Fatal("first bytes differ")
	}
	oldConnection := proxy.Negotiations()[2].Connection
	putProxy(t, st, "ntlm", p, "rotated-wrong-secret")
	second, err := pool.Client(c, resolveFor(t, resolver, c))
	if err != nil {
		t.Fatal(err)
	}
	response, err := second.Do(t.Context(), remote.Request{Method: http.MethodGet, Path: "item"})
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, remote.ErrSecurity) || first == second {
		t.Fatal("forward authentication survived secret rotation")
	}
	records := proxy.Negotiations()
	if len(records) != 6 || records[3].Connection == oldConnection {
		t.Fatal("rotated forwarding reused the authenticated socket")
	}
	putProxy(t, st, "ntlm", p, "proxy-secret")
	third, err := pool.Client(c, resolveFor(t, resolver, c))
	if err != nil {
		t.Fatal(err)
	}
	if body(t, getFor(t, third, "item")) != "forward rotation bytes" {
		t.Fatal("restored identity did not authenticate")
	}
	if origin.Count(http.MethodGet, "/item") != 2 {
		t.Fatal("wrong rotated credentials reached origin")
	}
}
