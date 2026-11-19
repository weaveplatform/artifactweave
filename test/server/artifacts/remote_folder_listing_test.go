package artifacts_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func remoteFolderWorld(t *testing.T, backend string) *harness.RemoteWorld {
	t.Helper()
	var mutate []func(*config.Config)
	if backend == "postgres" {
		dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
		mutate = append(mutate, func(c *config.Config) { c.Database = config.Database{Driver: "postgres", DSN: dsn} })
	}
	w := harness.NewRemoteWorld(t, mutate...)
	w.Repo.ListRemoteFolderItems = repo.Flex(true)
	w.Repo.RetrievalCachePeriodSecs = ptr(0)
	w.Update()
	return w
}

func remoteFolderCache(t *testing.T, w *harness.RemoteWorld, paths ...string) {
	t.Helper()
	for _, p := range paths {
		w.Upstream.Set("/"+p, fakeupstream.Resource{Body: []byte("cached " + p)})
		rec := w.Request("GET", "/artifactory/remote/"+p, "admin", "")
		if rec.Code != 200 {
			t.Fatalf("cache %s:%d %s", p, rec.Code, rec.Body)
		}
	}
}

func remoteFolderNames(rec *httptest.ResponseRecorder) []string {
	var names []string
	for _, m := range indexLinks.FindAllStringSubmatch(rec.Body.String(), -1) {
		names = append(names, m[1])
	}
	return names
}

func wantRemoteFolder(t *testing.T, w *harness.RemoteWorld, method, target, user string, code int, names []string) *httptest.ResponseRecorder {
	t.Helper()
	rec := w.Request(method, target, user, "")
	if rec.Code != code {
		t.Fatalf("%s %s:%d %s, want%d", method, target, rec.Code, rec.Body, code)
	}
	if code == 200 {
		if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("index Content-Type:%v", rec.Header())
		}
		if method == http.MethodHead {
			if rec.Body.Len() != 0 {
				t.Fatalf("HEAD disclosed body:%s", rec.Body)
			}
		} else if got := remoteFolderNames(rec); !slices.Equal(got, names) {
			t.Fatalf("%s: names%v want%v; body%s", target, got, names, rec.Body)
		}
	} else if strings.Contains(rec.Body.String(), "Index of") {
		t.Fatalf("error disclosed cached HTML:%s", rec.Body)
	}
	return rec
}

func TestAccRemoteFolderUnionOwnsCachedNames(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := remoteFolderWorld(t, backend)
			remoteFolderCache(t, w, "dir/cached.bin", "dir/same.bin", "dir/cache-folder/inside.bin")
			w.Upstream.Set("/dir/", upstreamIndex("live.bin", "same.bin/", "cache-folder", "remote-folder/", "dupe.bin", "dupe.bin"))
			want := []string{"cache-folder/", "cached.bin", "dupe.bin", "live.bin", "remote-folder/", "same.bin"}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				wantRemoteFolder(t, w, method, "/artifactory/remote/dir/", "admin", 200, want)
			}
			before := w.Upstream.Count("GET", "/dir/")
			wantRemoteFolder(t, w, "GET", "/artifactory/remote-cache/dir/", "admin", 200, []string{"cache-folder/", "cached.bin", "same.bin"})
			w.Repo.ListRemoteFolderItems = repo.Flex(false)
			w.Update()
			wantRemoteFolder(t, w, "GET", "/artifactory/remote/dir/", "admin", 200, []string{"cache-folder/", "cached.bin", "same.bin"})
			if w.Upstream.Count("GET", "/dir/") != before {
				t.Fatal("cache alias or disabled listing fetched origin")
			}
			w.Repo.ListRemoteFolderItems = repo.Flex(true)
			w.Repo.StoreArtifactsLocally = ptr(false)
			w.Update()
			wantRemoteFolder(t, w, "GET", "/artifactory/remote/dir/", "admin", 200, []string{"cache-folder", "dupe.bin", "live.bin", "remote-folder/", "same.bin/"})
			for _, p := range []string{"/dir/live.bin", "/dir/dupe.bin"} {
				if w.Upstream.Count("GET", p) != 0 {
					t.Fatalf("listing fetched child payload %s", p)
				}
			}
		})
	}
}

func TestAccRemoteFolderPresenceAndOriginFailure(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, tc := range []struct {
				name                                                                         string
				cached, emptyFolder, root, noStore, transport, emptyIndex, filtered, offline bool
				code                                                                         int
				want                                                                         []string
			}{
				{name: "cached-503", cached: true, code: 200, want: []string{"kept.bin"}},
				{name: "cached-transport", cached: true, transport: true, code: 200, want: []string{"kept.bin"}},
				{name: "cached-offline-without-index-cache", cached: true, offline: true, code: 200, want: []string{"kept.bin"}},
				{name: "absent-503", code: 404},
				{name: "empty-cache-folder-503", emptyFolder: true, code: 200},
				{name: "store-local-root-503", root: true, code: 200},
				{name: "no-store-ignores-existing-cache", cached: true, noStore: true, code: 404},
				{name: "empty-upstream-absent-cache", emptyIndex: true, code: 404},
				{name: "all-pattern-filtered-upstream", filtered: true, code: 200},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := remoteFolderWorld(t, backend)
					folder := "dir/"
					if tc.root {
						folder = ""
					}
					if tc.cached {
						remoteFolderCache(t, w, "dir/kept.bin")
					}
					if tc.emptyFolder {
						if _, err := w.Runtime.Store.Items().Put(t.Context(), model.Item{RepoKey: "remote", Cached: true, Name: "dir", Folder: true}); err != nil {
							t.Fatal(err)
						}
					}
					if tc.noStore {
						w.Repo.StoreArtifactsLocally = ptr(false)
						w.Update()
					}
					if tc.offline {
						w.Repo.Offline = ptr(true)
						w.Update()
					}
					response := fakeupstream.Resource{Status: 503}
					if tc.emptyIndex {
						response = fakeupstream.Resource{Body: []byte("<h1>Index of /dir/</h1>"), Header: http.Header{"Content-Type": {"text/html"}}}
					}
					if tc.filtered {
						response = upstreamIndex("hidden.secret")
						w.Repo.ExcludesPattern = ptr("**/*.secret")
						w.Update()
					}
					w.Upstream.Set("/"+folder, response)
					if tc.transport {
						w.Upstream.Close()
					}
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						rec := wantRemoteFolder(t, w, method, "/artifactory/remote/"+folder, "admin", tc.code, tc.want)
						if tc.code == 404 && method == http.MethodGet && rec.Body.String() != errorBody(t, 404, "Could not find resource") {
							t.Fatalf("folder miss:%s", rec.Body)
						}
					}
					if tc.offline && w.Upstream.Count("GET", "/"+folder) != 0 {
						t.Fatal("offline cached-folder browse reached origin")
					}
				})
			}
		})
	}
}

func TestAccRemoteFolderFaultPhases(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, tc := range []struct {
				name, table, folder string
				code                int
			}{
				{"source-authority", "remote_sources", "dir/", 500},
				{"cached-folder-get", "items", "dir/", 404},
				{"cached-root-children", "items", "", 404},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := remoteFolderWorld(t, backend)
					remoteFolderCache(t, w, "dir/kept.bin")
					w.Upstream.Set("/"+tc.folder, upstreamIndex("live.bin"))
					var logs bytes.Buffer
					w.H = harness.Router(
						harness.Config{
							Store:         w.Runtime.Store,
							Blobs:         w.Runtime.Blobs,
							Auth:          w.Runtime.Auth,
							Tokens:        w.Runtime.Tokens,
							Keys:          w.Runtime.Keys,
							RemoteSources: w.Runtime.Artifacts.RemoteSources(),
							RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
							VirtualCache:  w.Runtime.Artifacts.VirtualCache(),
							Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
						},
					)
					before := len(w.Upstream.Requests())
					db := w.Runtime.Store.DB()
					if _, err := db.ExecContext(t.Context(), "ALTER TABLE "+tc.table+" RENAME TO "+tc.table+"_held"); err != nil {
						t.Fatal(err)
					}
					defer func() {
						if _, err := db.ExecContext(context.Background(), "ALTER TABLE "+tc.table+"_held RENAME TO "+tc.table); err != nil {
							t.Error(err)
						}
					}()
					rec := wantRemoteFolder(t, w, "GET", "/artifactory/remote/"+tc.folder, "admin", tc.code, nil)
					message := "Could not find resource"
					if tc.code == 500 {
						message = "internal error"
					}
					if rec.Body.String() != errorBody(t, tc.code, message) {
						t.Fatalf("unsanitized fault:%s", rec.Body)
					}
					if len(w.Upstream.Requests()) != before {
						t.Fatal("failed local phase fetched upstream")
					}
					text := logs.String()
					if strings.Count(text, "level=ERROR") != 1 || !strings.Contains(text, tc.table) ||
						(!strings.Contains(text, "no such table") && !strings.Contains(text, "SQLSTATE 42P01")) {
						t.Fatalf("original SQL cause not logged once:%s", text)
					}
				})
			}
		})
	}
}
