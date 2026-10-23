package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	agentpluginsfmt "github.com/weaveplatform/artifactweave/internal/format/agentplugins"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func pluginZIP(t *testing.T, harness string) string {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("." + harness + "-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(
		[]byte(`{"name":"wrong","version":"99.0.0","description":"sample"}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestAgentPluginMarketplacesTrackTreeAndPermissions(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	setTrashcan(t, w.db, true)
	c := repo.Config{Key: "plugins-local", Class: repo.Local, PackageType: repo.AgentPlugins}
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"marketplace.json", "vscode-marketplace.json"} {
		wantStatus(t, w.do(http.MethodPut, "/artifactory/plugins-local/"+name,
			"admin", "{}"), http.StatusConflict)
	}
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "old-plugin-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"deployer": types.ActionRead}},
			Targets: map[string]types.Target{
				"plugins-local": {Include: []string{"reader/1.9.0/**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	oldPath := "/artifactory/plugins-local/reader/1.9.0/reader-1.9.0.zip"
	newPath := "/artifactory/plugins-local/reader/1.10.0/reader-1.10.0.zip"
	wantStatus(
		t,
		w.do(http.MethodPut, oldPath, "admin", pluginZIP(t, "claude")),
		http.StatusCreated,
	)
	wantStatus(
		t,
		w.do(http.MethodPut, newPath, "admin", pluginZIP(t, "cursor")),
		http.StatusCreated,
	)
	read := func(path, as string) map[string]any {
		t.Helper()
		r := w.do(http.MethodGet, path, as, "")
		wantStatus(t, r, http.StatusOK)
		var doc map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	entry := func(doc map[string]any) map[string]any {
		t.Helper()
		p := doc["plugins"].([]any)
		if len(p) != 1 {
			t.Fatalf("marketplace %+v", doc)
		}
		return p[0].(map[string]any)
	}
	api := "/artifactory/api/agentplugins/plugins-local/marketplace.json"
	if got := entry(read(api, "admin")); got["version"] != "1.10.0" || got["name"] != "reader" {
		t.Fatalf("latest %+v", got)
	}
	if got := entry(read(api, "deployer")); got["version"] != "1.9.0" {
		t.Fatalf("permission filter %+v", got)
	}
	if hits := hitsOf(t, w.do(http.MethodGet,
		"/artifactory/api/search/artifact?name=reader-1.10.0.zip",
		"deployer", "")); len(hits) != 0 {
		t.Fatalf("artifact search leaked denied plugin: %+v", hits)
	}
	if got := entry(
		read("/artifactory/plugins-local/claude-marketplace.json", "admin"),
	); got["version"] != "1.9.0" ||
		got["source"].(map[string]any)["source"] != "url" {
		t.Fatalf("Claude %+v", got)
	}
	if got := entry(
		read("/artifactory/plugins-local/cursor-marketplace.json", "admin"),
	); got["version"] != "1.10.0" {
		t.Fatalf("Cursor %+v", got)
	}
	indexed, err := w.db.Packages().Find(ctx, c.Key, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Packages().Delete(ctx, indexed.ID); err != nil {
		t.Fatal(err)
	}
	if err := (agentpluginsfmt.Handler{}).Reindex(
		ctx,
		format.Env{Store: w.db, Blobs: w.blobs},
		c,
	); err != nil {
		t.Fatal(err)
	}
	if got := entry(read(api, "admin")); got["version"] != "1.10.0" {
		t.Fatalf("reindex %+v", got)
	}
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/api/agentplugins/libs-local/marketplace.json", "admin", ""),
		http.StatusNotFound)
	wantStatus(t, w.do(http.MethodPut, newPath, "admin", pluginZIP(t, "claude")),
		http.StatusCreated)
	if plugins := read("/artifactory/plugins-local/cursor-marketplace.json", "admin")["plugins"].([]any); len(
		plugins,
	) != 0 {
		t.Fatalf("overwrite left stale Cursor entry: %+v", plugins)
	}
	wantStatus(t, w.do(http.MethodGet, api, "user", ""), http.StatusForbidden)
	wantStatus(
		t,
		w.do(
			http.MethodPut,
			"/artifactory/plugins-local/reader/2.0.0/reader-2.0.0.zip",
			"admin",
			"bad zip",
		),
		http.StatusConflict,
	)
	wantStatus(t, w.do(http.MethodDelete, newPath, "admin", ""), http.StatusNoContent)
	if got := entry(read(api, "admin")); got["version"] != "1.9.0" {
		t.Fatalf("delete left stale index %+v", got)
	}
	wantStatus(t, w.do(
		http.MethodPost,
		"/artifactory/api/trash/restore/plugins-local/reader/1.10.0/reader-1.10.0.zip?to=plugins-local",
		"admin",
		"",
	), http.StatusAccepted)
	if got := entry(read(api, "admin")); got["version"] != "1.10.0" {
		t.Fatalf("restore left stale index %+v", got)
	}
	wantStatus(t, w.do(
		http.MethodPost,
		"/artifactory/api/copy/plugins-local/reader/1.10.0/reader-1.10.0.zip?to=/plugins-local/reader/1.11.0/reader-1.11.0.zip",
		"admin",
		"",
	), http.StatusOK)
	if got := entry(read(api, "admin")); got["version"] != "1.11.0" {
		t.Fatalf("copy left stale index %+v", got)
	}
	if got := w.do(
		http.MethodHead,
		api,
		"admin",
		"",
	); got.Code != http.StatusOK ||
		got.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", got.Code, got.Body)
	}
	if got := w.do(
		http.MethodGet,
		"/artifactory/plugins-local/vscode-marketplace.json",
		"admin",
		"",
	); got.Code != http.StatusNotFound {
		t.Fatalf("unsupported target index: %d %s", got.Code, got.Body)
	}
	wantStatus(
		t,
		w.do(
			http.MethodPut,
			"/artifactory/plugins-local/reader/2.0.0/reader-2.0.0.zip",
			"admin",
			pluginZIP(t, "claude"),
		),
		http.StatusCreated,
	)
	if !strings.Contains(w.do(http.MethodGet, api, "admin", "").Body.String(), `"2.0.0"`) {
		t.Fatal("missing newly published version")
	}
}
