package pushhttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// A valid redirectedToHosts list is accepted by push but grants nothing: the
// outbound client follows only same-origin GET/HEAD redirects with the target's
// own credentials, and refuses cross-origin and write redirects outright.
func TestAccTargetRedirectedHostsKeepProxyUseWithoutGrant(t *testing.T) {
	const proxySecret = "proxy-redirect-private"
	targetAuth := fakeupstream.Auth{Username: "worker", Password: "fixture-secret"}
	proxyBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:"+proxySecret))
	for _, ntlm := range []bool{false, true} {
		t.Run("ntlm="+strconv.FormatBool(ntlm), func(t *testing.T) {
			origin := fakeupstream.New(t, fakeupstream.Options{Auth: targetAuth})
			// The other host accepts the proxy account, so any credential
			// substitution there would succeed rather than hide behind a 401.
			other := fakeupstream.New(t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "proxy-user", Password: proxySecret}})
			document := []byte(`{"repo":"repo","path":"/file","size":"5","lastModified":"2026-11-22T00:00:00Z","lastUpdated":"2026-11-22T00:00:00Z","checksums":{"sha256":"` +
				sums([]byte("proof")).SHA256 + `"},"properties":{"proof":["redirected"]}}`)
			origin.Set(deployment+"/api/storage/repo/file", fakeupstream.Resource{Redirect: origin.URL + deployment + "/api/storage/repo/moved"})
			origin.Set(deployment+"/api/storage/repo/moved", fakeupstream.Resource{Body: document})
			origin.Set(deployment+"/api/storage/repo/away", fakeupstream.Resource{Redirect: other.URL + deployment + "/api/storage/repo/moved"})
			origin.Set(
				deployment+"/repo/upload",
				fakeupstream.Resource{Method: http.MethodPut, Status: http.StatusTemporaryRedirect, Redirect: origin.URL + deployment + "/repo/elsewhere"},
			)
			other.Set(deployment+"/api/storage/repo/moved", fakeupstream.Resource{Body: document})
			var proxyURL string
			var negotiations func() int
			if ntlm {
				proxy := fakeupstream.NewNTLMProxy(t, fakeupstream.NTLMOptions{
					Username: "proxy-user", Password: proxySecret, Domain: "CORP", Workstation: "REPLICATOR",
				}, origin, other)
				proxyURL = proxy.URL
				negotiations = func() int {
					n := 0
					for _, record := range proxy.Negotiations() {
						if record.Message == 3 && record.Authorized {
							n++
						}
					}
					return n
				}
			} else {
				proxy := fakeupstream.NewProxy(t, fakeupstream.Auth{Username: "proxy-user", Password: proxySecret}, origin, other)
				proxyURL = proxy.URL
				negotiations = func() int { return proxy.Count(http.MethodConnect, "") }
			}
			address, err := url.Parse(proxyURL)
			if err != nil {
				t.Fatal(err)
			}
			proxyPort, err := strconv.Atoi(address.Port())
			if err != nil {
				t.Fatal(err)
			}
			proxy := wire.ProxyConfiguration{Host: address.Hostname(), Port: proxyPort, Username: "proxy-user", RedirectedToHosts: "127.0.0.1, localhost"}
			if ntlm {
				proxy.Domain, proxy.NTHost = "CORP", "REPLICATOR"
			}
			store := platforminmem.New(memtx.New())
			putProxy(t, store, "redirects", proxy, proxySecret)
			factory, err := New(store, Options{RevisionKey: bytes.Repeat([]byte{8}, 32), AllowedPrivateOrigins: []string{origin.URL, other.URL, proxyURL}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(factory.Close)
			target := fixtureTarget(origin.URL)
			target.Proxy = "redirects"
			var connection port.PushConnection
			if err := store.Tx(t.Context(), func(ctx context.Context) error {
				var err error
				connection, err = factory.Connection(ctx, target)
				return err
			}); err != nil {
				t.Fatal("valid redirected hosts disabled proxy use", err)
			}
			item, err := connection.Inspect(t.Context(), "file")
			if err != nil || item.Snapshot.Actual.SHA256 != sums([]byte("proof")).SHA256 {
				t.Fatal("same-origin GET redirect through the proxy failed", err)
			}
			if negotiations() == 0 {
				t.Fatal("target exchange bypassed its proxy")
			}
			if _, err := connection.Inspect(t.Context(), "away"); !errors.Is(err, outbound.ErrRedirect) {
				t.Fatal("cross-origin redirect to a listed host was followed", err)
			}
			body := []byte("redirected write")
			err = connection.Put(t.Context(), artifactsapi.TransferItem{Path: "upload", Size: int64(len(body)), Actual: sums(body)}, bytes.NewReader(body), allowMutation)
			if !errors.Is(err, outbound.ErrRedirect) {
				t.Fatal("write redirect was followed", err)
			}
			if other.Count("", "") != 0 || origin.Count("", deployment+"/repo/elsewhere") != 0 {
				t.Fatal("a redirect reached its destination")
			}
			for _, record := range origin.Requests() {
				if !record.Authorized || record.HeaderMatches("Authorization", proxyBasic) || record.Header.Get("Proxy-Authorization") != "" {
					t.Fatal("proxy credentials replaced or joined the target credentials")
				}
			}
		})
	}
}
