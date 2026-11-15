package npm_test

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// externalTarballWorld is harness.NewRemoteWorld's world with the npm remote
// "remote" at the path /api/v4/packages/npm of its upstream, and a second
// fake upstream, cdn, that the runtime may reach although it is on a private
// address, as an operator's allowed_private_origins lets it. The remote has
// external dependencies on with a pattern that names no URL in use, which
// an npm remote does not read.
func externalTarballWorld(t *testing.T) (*harness.RemoteWorld, *fakeupstream.Server) {
	t.Helper()
	cdn := fakeupstream.New(t, fakeupstream.Options{})
	rt := harness.App(t, func(cfg *config.Config) {
		cfg.Bootstrap.AdminPassword = "pw"
		cfg.Remote.AllowedPrivateOrigins = []string{cdn.URL}
	})
	up := fakeupstream.New(t, fakeupstream.Options{})
	c := *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.NPM,
		URL:      up.URL + "/api/v4/packages/npm",
		Username: "upstream-user", Password: "upstream-secret",
		ExternalDependenciesEnabled:  new(true),
		ExternalDependenciesPatterns: []string{"https://nowhere.example/**"},
	})
	if err := rt.Store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.Users().
		Create(t.Context(), accesstest.User{Name: "reader", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "remote-read", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{"remote": {}},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	h := harness.Router(harness.Config{
		Store: rt.Store, Blobs: rt.Blobs, Auth: rt.Auth, Tokens: rt.Tokens, Keys: rt.Keys,
		RemoteSources: rt.Artifacts.RemoteSources(),
		RemoteHTTP:    rt.Artifacts.RemoteHTTP(),
		Logger:        slog.New(slog.DiscardHandler),
	})
	return &harness.RemoteWorld{T: t, Runtime: rt, H: h, Upstream: up, Repo: c}, cdn
}

// TestAccNPMRemoteTarballOutsideRegistry: an npm remote lists a version
// whose dist.tarball lies outside its registry, on its host (RTFACT-23931's
// registry at /api/v4/packages/npm, with its tarballs under
// /api/v4/projects/<id>) or on another host, rewritten to the version's
// canonical path under the remote, and serves the tarball from where the
// packument puts it, without sending the remote's credentials to another
// host. 7.x stopped answering 404 for such a tarball in 7.24.1
// (RTFACT-23931), and 7.161.26 uses an npm handler's download URL on
// another host verbatim (RemoteRepoBase.mayReplaceConfiguredOrigin), so no
// repository field gates it; the packument, version, dist-tags and tarball
// reads answered 502 {"error":"Bad Gateway"} while the remote's
// externalDependencies fields, which 7.x keeps for npm virtual repositories
// alone, had no pattern matching the URL (#1574).
func TestAccNPMRemoteTarballOutsideRegistry(t *testing.T) {
	for name, host := range map[string]string{"same host": "up", "other host": "cdn"} {
		t.Run(name, func(t *testing.T) {
			w, cdn := externalTarballWorld(t)
			data := support.NPMTarballOf("pkg", "1.0.0")
			sum := sha512.Sum512(data)
			integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
			from, file := w.Upstream, "/api/v4/projects/1886/packages/npm/pkg/-/pkg-1.0.0.tgz"
			if host == "cdn" {
				from, file = cdn, "/tarballs/pkg-1.0.0.tgz"
			}
			from.Set(file, fakeupstream.Resource{
				Body: data, Header: http.Header{"Content-Type": {"application/octet-stream"}},
			})
			doc, _ := json.Marshal(map[string]any{
				"name": "pkg",
				"versions": map[string]any{"1.0.0": map[string]any{
					"name": "pkg", "version": "1.0.0",
					"dist": map[string]string{"tarball": from.URL + file, "integrity": integrity},
				}},
				"dist-tags": map[string]string{"latest": "1.0.0"},
			})
			w.Upstream.Set("/api/v4/packages/npm/pkg", fakeupstream.Resource{
				Body: doc, Header: http.Header{"Content-Type": {"application/json"}},
			})

			const remote = "/artifactory/api/npm/remote/"
			version := `{"dist":{"integrity":"` + integrity + `",` +
				`"tarball":"http://example.com/artifactory/api/npm/remote/pkg/-/pkg-1.0.0.tgz"},` +
				`"name":"pkg","version":"1.0.0"}`
			for path, want := range map[string]string{
				"pkg": `{"dist-tags":{"latest":"1.0.0"},"name":"pkg","versions":{"1.0.0":` +
					version + `}}`,
				"pkg/1.0.0":               version,
				"-/package/pkg/dist-tags": `{"latest":"1.0.0"}`,
			} {
				rec := w.Request(http.MethodGet, remote+path, "reader", "")
				harness.WantStatus(t, rec, http.StatusOK)
				if got := rec.Body.String(); got != want+"\n" {
					t.Fatalf("GET %s answers %s, want %s", path, got, want)
				}
			}
			rec := w.Request(http.MethodGet, remote+"pkg/-/pkg-1.0.0.tgz", "reader", "")
			harness.WantStatus(t, rec, http.StatusOK)
			if rec.Body.String() != string(data) || from.Count(http.MethodGet, file) != 1 {
				t.Fatalf("the tarball was not fetched from %s: %v", file, from.Requests())
			}
			for _, r := range cdn.Requests() {
				if r.Header.Get("Authorization") != "" {
					t.Fatalf("the remote's credentials went to another host: %v", r.Header)
				}
			}
		})
	}
}
