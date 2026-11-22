package generic_test

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	remoteupstream "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccProxyRedirectPublicationAndRotation(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			target := fakeupstream.New(t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}})
			w := harness.NewRemoteWorld(t, func(cfg *config.Config) {
				cfg.MasterKey = "0123456789abcdef0123456789abcdef"
				cfg.Remote.AllowedPrivateOrigins = []string{target.URL}
				if backend == "postgres" {
					cfg.Database.Driver = "postgres"
					cfg.Database.DSN = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
				}
			})
			st := w.Runtime.Store
			for _, path := range []string{"artifact", "broken", "held", "removed", "malformed", "rotated"} {
				w.Upstream.Set("/"+path, fakeupstream.Resource{Redirect: target.URL + "/" + path})
				target.Set("/"+path, fakeupstream.Resource{Body: []byte("complete artifact")})
			}
			target.Set("/broken", fakeupstream.Resource{Body: []byte("incomplete artifact"), Fault: fakeupstream.Fault{TruncateAfter: 4}})
			u, err := url.Parse(target.URL)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp6", net.JoinHostPort("::1", u.Port()))
			if err != nil {
				t.Fatal(err)
			}
			proxy := fakeupstream.NewProxyOn(t, listener, fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}, w.Upstream, target)
			port, err := strconv.Atoi(u.Port())
			if err != nil {
				t.Fatal(err)
			}
			p := wire.ProxyConfiguration{Host: "http://[::1]", Port: port, Username: "proxy-user", RedirectedToHosts: "127.0.0.1"}
			put := func(secret string) {
				t.Helper()
				document, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Configs().
					Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigProxy, Key: "redirect", Enabled: true, Document: document, Secrets: map[string]string{"password": secret}}); err != nil {
					t.Fatal(err)
				}
			}
			put("proxy-secret")
			w.Repo.Proxy = "redirect"
			w.Repo.BypassHeadRequests = new(true)
			w.Update()
			resolver, err := remoteupstream.NewResolver(st, w.Runtime.Keys, remoteupstream.ResolverOptions{AllowedPrivateOrigins: []string{target.URL}})
			if err != nil {
				t.Fatal(err)
			}
			options := func() string {
				t.Helper()
				opts, err := resolver.Options(t.Context(), w.Repo)
				if err != nil {
					t.Fatal(err)
				}
				return opts.Revision
			}
			read := func(path string) *httptest.ResponseRecorder {
				return w.Request(http.MethodGet, "/artifactory/remote/"+path, "reader", "")
			}
			assertMissing := func(path string) {
				t.Helper()
				if _, err := st.Items().Get(t.Context(), w.Repo.Key, true, path); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("failed fetch published an item", err)
				}
				entries, _, err := st.RemoteCache().Exact(t.Context(), w.Repo.Key, path, kernel.Page{Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Blob.Checksums.SHA256 != "" || !entry.NegativeUntil.IsZero() {
						t.Fatal("failed fetch published content or a negative result")
					}
				}
				if response := w.Request("GET", "/artifactory/remote-cache/"+path, "reader", ""); response.Code != 404 {
					t.Fatalf("failed fetch became readable: %d", response.Code)
				}
			}
			initial := options()
			response := read("artifact")
			if response.Code != 200 || response.Body.String() != "complete artifact" {
				t.Fatalf("complete fetch: %d %s", response.Code, response.Body)
			}
			if response := read("broken"); response.Code == 200 {
				t.Fatal("partial fetch succeeded")
			}
			assertMissing("broken")
			gate := new(fakeupstream.Barrier)
			defer gate.Release()
			target.Set("/held", fakeupstream.Resource{Body: []byte("late complete artifact"), Fault: fakeupstream.Fault{BeforeBody: gate}})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- read("held") }()
			select {
			case <-gate.Reached():
			case <-time.After(10 * time.Second):
				t.Fatal("redirect body did not block")
			}
			p.RedirectedToHosts = ""
			put("proxy-secret")
			removed := options()
			if removed == initial {
				t.Fatal("removal retained source revision")
			}
			gate.Release()
			if response := <-done; response.Code != http.StatusServiceUnavailable {
				t.Fatalf("stale publication: %d %s", response.Code, response.Body)
			}
			assertMissing("held")
			if response := read("artifact"); response.Code == http.StatusOK {
				t.Fatal("normal read reused bytes fetched under removed credentials")
			}
			if response := read("removed"); response.Code == 200 {
				t.Fatal("removed credentials remained effective")
			}
			assertMissing("removed")
			p.RedirectedToHosts = "127.0.0.1"
			put("rotated-proxy-secret")
			rotated := options()
			if rotated == initial || rotated == removed {
				t.Fatal("secret rotation retained source revision")
			}
			before := target.Count("", "")
			if response := read("rotated"); response.Code == 200 || strings.Contains(response.Body.String(), "rotated-proxy-secret") {
				t.Fatal("rotation succeeded or exposed secret")
			}
			if target.Count("", "") != before {
				t.Fatal("wrong proxy credentials reached target")
			}
			assertMissing("rotated")
			put("proxy-secret")
			if response := read("held"); response.Code != 200 || response.Body.String() != "late complete artifact" {
				t.Fatalf("restored setting: %d %s", response.Code, response.Body)
			}
			for _, malformed := range []string{"*.example", "\u00a0127.0.0.1", "127.0.0.1\u00a0", "\u0130.example"} {
				p.RedirectedToHosts = malformed
				put("proxy-secret")
				before = proxy.Count("", "")
				if response := read("malformed"); response.Code == 200 || strings.Contains(response.Body.String(), "proxy-secret") {
					t.Fatal("malformed setting succeeded or exposed secret")
				}
				if proxy.Count("", "") != before {
					t.Fatal("malformed host reached network")
				}
				assertMissing("malformed")
			}
		})
	}
}
