package federation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccSinglePullCatalogProcessesSQLite(t *testing.T) {
	qualifyPullCatalogProcesses(t, "sqlite", false, false)
}

func TestAccSinglePullCatalogProcessesPostgres(t *testing.T) {
	qualifyPullCatalogProcesses(t, "postgres", false, false)
}

func TestAccSinglePullVirtualCatalogProcessesSQLite(t *testing.T) {
	qualifyPullCatalogProcesses(t, "sqlite", true, false)
}

func TestAccSinglePullVirtualCatalogProcessesPostgres(t *testing.T) {
	qualifyPullCatalogProcesses(t, "postgres", true, false)
}

func TestAccSinglePullCacheProcessesSQLite(t *testing.T) {
	qualifyPullCatalogProcesses(t, "sqlite", false, true)
}

func TestAccSinglePullCacheProcessesPostgres(t *testing.T) {
	qualifyPullCatalogProcesses(t, "postgres", false, true)
}

func TestAccSinglePullVirtualCacheProcessesSQLite(t *testing.T) {
	qualifyPullCatalogProcesses(t, "sqlite", true, true)
}

func TestAccSinglePullVirtualCacheProcessesPostgres(t *testing.T) {
	qualifyPullCatalogProcesses(t, "postgres", true, true)
}

func qualifyPullCatalogProcesses(t *testing.T, driver string, virtual, populate bool) {
	t.Helper()
	for _, packageType := range []string{"generic", "maven"} {
		t.Run(packageType, func(t *testing.T) {
			var localDSN, peerDSN string
			if driver == "postgres" {
				dsn := sqltest.PostgresDSN(t)
				localDSN, peerDSN = sqltest.ScratchDSN(t, dsn), sqltest.ScratchDSN(t, dsn)
			}
			w := newPushWorld(t, driver, localDSN, peerDSN, packageType)
			path, body, root := "selected/a;b?c#d% space+é.bin", "verified peer bytes", "selected"
			if packageType == "maven" {
				path, root = "com/acme/app/1.0/app-1.0.pom", "com/acme"
				body = `<project><modelVersion>4.0.0</modelVersion><groupId>com.acme</groupId><artifactId>app</artifactId><version>1.0</version></project>`
			}
			w.request(w.target, http.MethodPut, itemRoute("target", path), body, http.StatusCreated)
			metadataPath := strings.TrimPrefix(itemRoute("target", path), "/artifactory/target/")
			w.request(
				w.target,
				http.MethodPatch,
				"/artifactory/api/metadata/target/"+metadataPath+"?recursiveProperties=0",
				`{"props":{"quality":["verified"]}}`,
				http.StatusNoContent,
			)
			peerKey := "target"
			if virtual {
				peerKey = "pull-view"
				configuration, err := json.Marshal(map[string]any{"rclass": "virtual", "packageType": packageType, "repositories": []string{"target"}})
				if err != nil {
					t.Fatal(err)
				}
				w.request(w.target, http.MethodPut, "/artifactory/api/repositories/"+peerKey, string(configuration), http.StatusOK)
			}
			configuration, err := json.Marshal(map[string]any{
				"rclass": "remote", "packageType": packageType, "url": w.bridge.URL + "/artifactory/" + peerKey,
				"username": "receiver", "password": "pw",
			})
			if err != nil {
				t.Fatal(err)
			}
			w.request(w.source, http.MethodPut, "/artifactory/api/repositories/pull-remote", string(configuration), http.StatusOK)
			actions := accessapi.ActionRead
			if populate {
				actions |= accessapi.ActionWrite | accessapi.ActionAnnotate | accessapi.ActionDelete
			}
			if err = w.runtime.Store.Permissions().Create(t.Context(), accesstest.Permission{Name: "pull-current-reader", Resources: map[accessapi.ResourceType]accesstest.Resource{
				accessapi.ResourceArtifact: {
					Targets: map[string]accesstest.Target{"pull-remote": {Include: []string{root + "/**"}}},
					Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"replica-reader": actions}},
				},
			}}); err != nil {
				t.Fatal(err)
			}
			principal, err := w.runtime.Auth.Basic(t.Context(), "replica-reader", "pw")
			if err != nil {
				t.Fatal(err)
			}
			ctx := accessapi.WithCaller(t.Context(), principal, nil)
			catalog := w.runtime.Artifacts.API().PullSource
			include := "**/*.bin"
			if packageType == "maven" {
				include = "**/*.pom"
			}
			selection := artifactsapi.PullSelection{Root: root, Include: include, Properties: true}
			repository, err := catalog.Repository(ctx, "pull-remote", selection)
			if err != nil {
				t.Fatal("bind real owning remote", err)
			}
			if w.bridge.count(http.MethodGet, "/artifactory/api/storage/"+peerKey+"/"+root) != 0 {
				t.Fatal("binding opened a peer connection")
			}
			items, next, err := catalog.Items(ctx, repository, selection, kernel.Page{Limit: 10})
			sum := sha256.Sum256([]byte(body))
			if err != nil || next != "" || len(items) != 1 || items[0].Path != path || items[0].Size != int64(len(body)) || items[0].Actual.SHA256 != hex.EncodeToString(sum[:]) ||
				items[0].Version == "" {
				t.Fatalf("real peer catalog %+v next=%q err=%v", items, next, err)
			}
			if len(items[0].Properties["quality"]) != 1 || items[0].Properties["quality"][0] != "verified" {
				t.Fatal("separate peer property snapshot was not retained")
			}
			if w.bridge.count(http.MethodGet, "/artifactory/target/"+path) != 0 {
				t.Fatal("catalog downloaded the selected artifact")
			}
			children, _, err := w.runtime.Store.Items().Children(ctx, "pull-remote", true, "", kernel.Page{})
			if err != nil || len(children) != 0 {
				t.Fatal("catalog manufactured cache state", err)
			}
			var proof artifactsapi.PullCached
			if populate {
				publication := artifactsapi.PullPublication{
					Check: func(context.Context) error { return nil },
					Fence: func(tx context.Context) error {
						if !w.runtime.Store.InTx(tx) {
							t.Fatal("cache publication escaped transaction")
						}
						return nil
					},
				}
				proof, err = catalog.Fetch(ctx, items[0], publication)
				if err != nil || proof.Version == "" || proof.Representation == "" {
					t.Fatal("cold pull did not retain ordinary cache provenance", err)
				}
				qualifyPulledCache(t, w, ctx, catalog, items[0], proof, publication, peerKey, path, body)
			}
			before := w.bridge.count(http.MethodGet, "/artifactory/api/storage/"+peerKey+"/"+root)
			if err = w.runtime.Store.Permissions().Delete(t.Context(), "pull-current-reader"); err != nil {
				t.Fatal(err)
			}
			if _, _, err = catalog.Items(
				ctx,
				repository,
				selection,
				kernel.Page{},
			); !errors.Is(err, kernel.ErrForbidden) ||
				w.bridge.count(http.MethodGet, "/artifactory/api/storage/"+peerKey+"/"+root) != before {
				t.Fatal("revoked reader reused a saved pull binding to reach the peer", err)
			}
			if populate {
				called := false
				if err = catalog.WithCurrent(ctx, proof, func(context.Context) error { called = true; return nil }); !errors.Is(err, kernel.ErrForbidden) || called {
					t.Fatal("revoked caller reused a committed cache receipt", err)
				}
			}
		})
	}
}

func qualifyPulledCache(t *testing.T, w *pushWorld, ctx context.Context, catalog artifactsapi.PullSource,
	selected artifactsapi.PullItem, proof artifactsapi.PullCached, publication artifactsapi.PullPublication,
	peerKey, path, body string,
) {
	t.Helper()
	if count := w.bridge.count(http.MethodGet, "/artifactory/"+peerKey+"/"+path); count != 1 {
		t.Fatalf("cold pull payload requests: got %d want 1", count)
	}
	for _, key := range []string{"pull-remote", "pull-remote-cache"} {
		data, _ := w.request(w.source, http.MethodGet, itemRoute(key, path), "", http.StatusOK)
		if string(data) != body {
			t.Fatal("ordinary/cache-alias read did not reuse verified pull bytes")
		}
	}
	if err := catalog.WithCurrent(ctx, proof, func(tx context.Context) error {
		if !w.runtime.Store.InTx(tx) {
			t.Fatal("cache checkpoint escaped transaction")
		}
		return nil
	}); err != nil {
		t.Fatal("committed cache proof was not current", err)
	}
	if _, err := catalog.Fetch(ctx, selected, publication); err != nil {
		t.Fatal("warm pull", err)
	}
	if count := w.bridge.count(http.MethodGet, "/artifactory/"+peerKey+"/"+path); count != 1 {
		t.Fatalf("warm pull or native read fetched bytes again: got %d want 1", count)
	}
	stored, err := w.runtime.Store.Items().Get(ctx, "pull-remote", true, path)
	if err != nil || stored.Size != selected.Size || stored.Actual.SHA256 != selected.Actual.SHA256 {
		t.Fatal("pull did not populate the real owning cache", err)
	}
	props, err := w.runtime.Store.Items().Properties(ctx, stored.ID)
	if err != nil || len(props["quality"]) != 1 || props["quality"][0] != "verified" {
		t.Fatal("selected properties did not accompany cache progress", err)
	}
	qualifyPulledProperties(t, w, ctx, catalog, selected, publication, peerKey, path)
	qualifyPulledRemoval(t, w, ctx, catalog, selected, publication, peerKey, path)
}

func qualifyPulledRemoval(t *testing.T, w *pushWorld, ctx context.Context, catalog artifactsapi.PullSource,
	selected artifactsapi.PullItem, grant artifactsapi.PullPublication, peerKey, path string,
) {
	t.Helper()
	candidates, next, err := catalog.CachedItems(ctx, selected.Repository, selected.Selection, kernel.Page{Limit: 10})
	if err != nil || next != "" || len(candidates) != 1 || candidates[0].Path != path {
		t.Fatal("real cache deletion candidate", err)
	}
	w.request(w.target, http.MethodDelete, itemRoute("target", path), "", http.StatusNoContent)
	refused := grant
	refused.Fence = func(context.Context) error { return kernel.ErrConflict }
	if _, err := catalog.Remove(ctx, candidates[0], refused, nil); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("expired worker deleted real cache state", err)
	}
	if _, err := w.runtime.Store.Items().Get(ctx, "pull-remote", true, path); err != nil {
		t.Fatal("lost lease did not roll back SQL cache deletion", err)
	}
	paths, err := catalog.Remove(ctx, candidates[0], grant, func(tx context.Context, paths []string) error {
		if !w.runtime.Store.InTx(tx) || len(paths) == 0 {
			t.Fatal("cache deletion checkpoint escaped SQL transaction")
		}
		return w.runtime.Store.Settings().Put(tx, "verified-pull-removal", true)
	})
	if err != nil || len(paths) == 0 {
		t.Fatal("verified SQL cache deletion", err)
	}
	w.request(w.source, http.MethodGet, itemRoute("pull-remote-cache", path), "", http.StatusNotFound)
	if count := w.bridge.count(http.MethodDelete, "/artifactory/"+peerKey+"/"+path); count != 0 {
		t.Fatal("cache reconciliation issued an upstream DELETE")
	}
}

func qualifyPulledProperties(t *testing.T, w *pushWorld, ctx context.Context, catalog artifactsapi.PullSource,
	selected artifactsapi.PullItem, publication artifactsapi.PullPublication, peerKey, path string,
) {
	t.Helper()
	escaped := strings.TrimPrefix(itemRoute("target", path), "/artifactory/target/")
	w.request(w.source, http.MethodPatch, "/artifactory/api/metadata/pull-remote-cache/"+escaped+"?recursiveProperties=0",
		`{"props":{"quality":["local"],"local-note":["kept"]}}`, http.StatusNoContent)
	w.request(w.target, http.MethodPatch, "/artifactory/api/metadata/target/"+escaped+"?recursiveProperties=0",
		`{"props":{"quality":["upstream-2"],"upstream-note":["new"]}}`, http.StatusNoContent)
	items, next, err := catalog.Items(ctx, selected.Repository, selected.Selection, kernel.Page{Limit: 10})
	if err != nil || next != "" || len(items) != 1 {
		t.Fatal("updated peer property selection", err)
	}
	if _, err = catalog.Fetch(ctx, items[0], publication); err != nil {
		t.Fatal("warm property synchronization", err)
	}
	stored, err := w.runtime.Store.Items().Get(ctx, "pull-remote", true, path)
	if err != nil {
		t.Fatal(err)
	}
	props, err := w.runtime.Store.Items().Properties(ctx, stored.ID)
	if err != nil || strings.Join(props["quality"], ",") != "local" || strings.Join(props["local-note"], ",") != "kept" ||
		strings.Join(props["upstream-note"], ",") != "new" {
		t.Fatal("pull synchronization lost local annotation ownership or upstream changes", err)
	}
	w.request(w.target, http.MethodPatch, "/artifactory/api/metadata/target/"+escaped+"?recursiveProperties=0",
		`{"props":{"upstream-note":["refused"]}}`, http.StatusNoContent)
	items, _, err = catalog.Items(ctx, selected.Repository, selected.Selection, kernel.Page{Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatal("refused peer property selection", err)
	}
	publication.Fence = func(context.Context) error { return kernel.ErrConflict }
	if _, err = catalog.Fetch(ctx, items[0], publication); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("expired worker published property success", err)
	}
	props, err = w.runtime.Store.Items().Properties(ctx, stored.ID)
	if err != nil || strings.Join(props["upstream-note"], ",") != "new" {
		t.Fatal("failed final fence did not roll back property changes", err)
	}
	if count := w.bridge.count(http.MethodGet, "/artifactory/"+peerKey+"/"+path); count != 1 {
		t.Fatal("property synchronization redownloaded verified bytes")
	}
}
