package generic_test

// This file holds the cache fill suite of #1587, a remote's cache filled
// only for a caller with Deploy/Cache on it, and the assumed-offline suite
// of #1601, a remote whose upstream is assumed offline answered from its
// cache alone.

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// readOnly adds the user "viewer", password "pw", whom a permission lets
// read the remote and nothing more, and lets anonymous callers read it
// too, with anonymous access on.
func readOnly(t *testing.T, w *remoteWorld) {
	t.Helper()
	ctx := t.Context()
	if err := w.rt.Store.Users().
		Create(ctx, accesstest.User{Name: "viewer", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Permissions().Create(ctx, accesstest.Permission{
		Name: "remote-view",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				"viewer": api.ActionRead, kernel.AnonymousUser: api.ActionRead,
			}},
			Targets: map[string]accesstest.Target{"remote": {}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
}

// TestAccRemoteReadAloneDoesNotFillTheCache (#1587): Read on a remote
// serves what its cache holds and fills it with nothing. A file the
// upstream holds and the cache does not answers 403 "Rejected artifact
// download request: " and RepositoryServiceImpl.
// assertValidDeployPathAndPermissions' reason for the cache path, which
// RemoteRepoBase.downloadAndSave checks before it downloads, with no path
// suffix (DownloadServiceImpl.respondFoundResource); an anonymous caller,
// the 401 challenge. The upstream is asked for the file's info, a HEAD,
// and nothing is kept. A file the upstream misses keeps its 404, and the
// copy a caller with Deploy/Cache fills is served to all. Before, the
// reader filled the cache: 200.
func TestAccRemoteReadAloneDoesNotFillTheCache(t *testing.T) {
	w := newRemoteWorld(t)
	readOnly(t, w)
	w.up.Set("/pkg/file.bin", fakeupstream.Resource{Body: []byte("upstream bytes")})
	const refused = "Rejected artifact download request: User viewer is not permitted to deploy " +
		"'pkg/file.bin' into 'remote-cache:pkg/file.bin'."
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		wantError(t, method, w.request(method, "/artifactory/remote/pkg/file.bin", "viewer", ""),
			http.StatusForbidden, refused)
		rec := w.request(method, "/artifactory/remote/pkg/file.bin", "", "")
		wantError(t, method, rec, http.StatusUnauthorized, "Unauthorized")
		if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="Artifactory Realm"` {
			t.Fatalf("anonymous %s: challenge %q", method, got)
		}
		wantError(t, method, w.request(method, "/artifactory/remote/pkg/missing.bin", "viewer", ""),
			http.StatusNotFound, "Not Found; Path: 'remote:pkg/missing.bin'")
	}
	if n := w.up.Count(http.MethodGet, "/pkg/file.bin"); n != 0 {
		t.Fatalf("a read-only caller downloaded the file: %v", w.up.Requests())
	}
	wantError(
		t,
		http.MethodGet,
		w.request(http.MethodGet, "/artifactory/remote-cache/pkg/file.bin", "viewer", ""),
		http.StatusNotFound,
		"File not found.; Path: 'remote-cache:pkg/file.bin'",
	)

	remoteStatus(
		t,
		w.request(http.MethodGet, "/artifactory/remote/pkg/file.bin", "reader", ""),
		http.StatusOK,
	)
	asked := len(w.up.Requests())
	for _, user := range []string{"viewer", ""} {
		rec := w.request(http.MethodGet, "/artifactory/remote/pkg/file.bin", user, "")
		if rec.Code != http.StatusOK || rec.Body.String() != "upstream bytes" {
			t.Fatalf("%q after the fill: %d %s", user, rec.Code, rec.Body)
		}
	}
	if len(w.up.Requests()) != asked {
		t.Fatalf("a cached file asked the upstream: %v", w.up.Requests())
	}
}

// TestAccRemoteAssumedOfflineAnswersFromTheCache (#1601): once connection
// failures have the upstream assumed offline, a file the cache does not
// hold answers 404 with RemoteRepoBase.internalGetInfo's "<key>: is
// assumed offline, '<key>:<path>' is not found at '<path>'." and the path
// suffix, GET and HEAD, a checksum naming its file; an expired copy, here
// a Maven remote's maven-metadata.xml, is served with hardFail on or off;
// and the upstream is not asked. Before, the miss answered 502 "Bad
// Gateway", and the expired copy 502 under hardFail.
func TestAccRemoteAssumedOfflineAnswersFromTheCache(t *testing.T) {
	const (
		meta     = "/org/acme/lib/maven-metadata.xml"
		metadata = "<metadata><version>1.0</version></metadata>"
		miss     = "remote: is assumed offline, 'remote:org/acme/lib/1.0/lib-1.0.jar' is not found at " +
			"'org/acme/lib/1.0/lib-1.0.jar'.; Path: 'remote:org/acme/lib/1.0/lib-1.0.jar'"
	)
	for _, hard := range []bool{true, false} {
		w := newRemoteWorld(t)
		remoteOfType(t, w, repo.Maven, func(c *repo.Config) {
			// A period of zero leaves the metadata expired once it is kept.
			c.RetrievalCachePeriodSecs = new(0)
			c.HardFail = new(hard)
		})
		w.up.Set(meta, fakeupstream.Resource{
			Body: []byte(metadata), Header: http.Header{"Content-Type": {"application/xml"}},
		})
		remoteStatus(
			t,
			w.request(http.MethodGet, "/artifactory/remote"+meta, "reader", ""),
			http.StatusOK,
		)
		const jar = "/org/acme/lib/1.0/lib-1.0.jar"
		w.up.Set(jar, fakeupstream.Resource{Fault: fakeupstream.Fault{DropConnection: true}})
		for range 10 {
			if rec := w.request(
				http.MethodGet,
				"/artifactory/remote"+jar,
				"reader",
				"",
			); rec.Code != http.StatusBadGateway {
				t.Fatalf("connection failure: %d %s", rec.Code, rec.Body)
			}
		}
		asked := len(w.up.Requests())
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			wantError(t, method, w.request(method, "/artifactory/remote"+jar, "reader", ""),
				http.StatusNotFound, miss)
			wantError(t, method, w.request(method, "/artifactory/remote"+jar+".sha1", "reader", ""),
				http.StatusNotFound, miss)
		}
		rec := w.request(http.MethodGet, "/artifactory/remote"+meta, "reader", "")
		if rec.Code != http.StatusOK || rec.Body.String() != metadata {
			t.Fatalf(
				"hardFail=%v: expired copy while assumed offline: %d %s",
				hard,
				rec.Code,
				rec.Body,
			)
		}
		if len(w.up.Requests()) != asked {
			t.Fatalf("an assumed-offline upstream was asked: %v", w.up.Requests()[asked:])
		}
	}
}
