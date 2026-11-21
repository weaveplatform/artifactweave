package federation_test

import (
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
	qualifyPullCatalogProcesses(t, "sqlite", false)
}

func TestAccSinglePullCatalogProcessesPostgres(t *testing.T) {
	qualifyPullCatalogProcesses(t, "postgres", false)
}

func TestAccSinglePullVirtualCatalogProcessesSQLite(t *testing.T) {
	qualifyPullCatalogProcesses(t, "sqlite", true)
}

func TestAccSinglePullVirtualCatalogProcessesPostgres(t *testing.T) {
	qualifyPullCatalogProcesses(t, "postgres", true)
}

func qualifyPullCatalogProcesses(t *testing.T, driver string, virtual bool) {
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
			if err = w.runtime.Store.Permissions().Create(t.Context(), accesstest.Permission{Name: "pull-current-reader", Resources: map[accessapi.ResourceType]accesstest.Resource{
				accessapi.ResourceArtifact: {
					Targets: map[string]accesstest.Target{"pull-remote": {Include: []string{root + "/**"}}},
					Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"replica-reader": accessapi.ActionRead}},
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
		})
	}
}
