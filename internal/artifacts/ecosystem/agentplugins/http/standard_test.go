package agentpluginshttp

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentplugins"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// standardPlugin indexes version of the plugin name in plugins-local as
// a bundle with a conforming root plugin.json and the manifests of
// harnesses, and queues the marketplace work, as the format's Deployed
// hook does for one.
func (w *itemWorld) standardPlugin(name, version string, harnesses ...string) {
	w.t.Helper()
	ctx := w.t.Context()
	p, err := w.world.Store.Packages().Ensure(ctx, model.Package{
		RepoKey: pluginsLocal, Type: repo.AgentPlugins, Name: name, Created: indexed,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	meta, err := json.Marshal(agentplugins.Metadata{
		Name:      name,
		Version:   version,
		Path:      agentplugins.Path(name, version),
		Harnesses: harnesses,
		Root:      true,
		Standard: json.RawMessage(
			`{"$schema":"` + agentplugins.StandardSchema + `","name":"` + name + `"}`,
		),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.world.Store.Packages().PutVersion(ctx, model.PackageVersion{
		PackageID: p.ID, Version: version, Created: indexed, Metadata: meta,
	}); err != nil {
		w.t.Fatal(err)
	}
	if err := w.world.Store.WorkQueue().Enqueue(
		ctx, agentplugins.KindMarketplace, pluginsLocal, nil, indexed,
	); err != nil {
		w.t.Fatal(err)
	}
}

// TestAccAgentPluginsVSCodeMarketplaceListsTheStandardLayout: self-managed
// 7.173.8 stores vscode-marketplace.json, served at both paths, listing the
// plugins whose newest version has a conforming root plugin.json, and adds
// those plugins to cursor-marketplace.json. The harness files and the
// unified file list what they listed before. A caller who may not read
// some of the ZIPs reads the same stored files.
func TestAccAgentPluginsVSCodeMarketplaceListsTheStandardLayout(t *testing.T) {
	w := newItemWorld(t)
	w.repos(pluginsRepo(pluginsLocal, repo.Local))
	w.plugin("alpha", "1.0.0", "claude")
	w.standardPlugin("alpha", "2.0.0")
	w.standardPlugin("beta", "1.0.0", "claude", "cursor")
	w.plugin("gamma", "1.0.0", "cursor")
	w.standardPlugin("hidden", "1.0.0")
	reader := &caller{kernel.Principal{Name: "reader"}, &accessapitest.Decider{
		AllowsFolderFunc: func(
			accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions,
		) bool {
			return true
		},
		AllowsFunc: func(
			_ accessapi.ResourceType, _ accessapi.Repo, p string, _ accessapi.Actions,
		) bool {
			return !strings.HasPrefix(p, "alpha/2.0.0/") && !strings.HasPrefix(p, "hidden/")
		},
	}}
	for _, tc := range []struct {
		file string
		as   *caller
		want []string
	}{
		{"vscode-marketplace.json", admin, []string{"alpha@2.0.0", "beta@1.0.0", "hidden@1.0.0"}},
		{
			"cursor-marketplace.json", admin,
			[]string{"alpha@2.0.0", "beta@1.0.0", "gamma@1.0.0", "hidden@1.0.0"},
		},
		{
			"marketplace.json", admin,
			[]string{"alpha@2.0.0", "beta@1.0.0", "gamma@1.0.0", "hidden@1.0.0"},
		},
		{"claude-marketplace.json", admin, []string{"alpha@1.0.0", "beta@1.0.0"}},
		{"codex-marketplace.json", admin, []string{}},
		{"vscode-marketplace.json", reader, []string{"alpha@2.0.0", "beta@1.0.0", "hidden@1.0.0"}},
		{
			"cursor-marketplace.json", reader,
			[]string{"alpha@2.0.0", "beta@1.0.0", "gamma@1.0.0", "hidden@1.0.0"},
		},
	} {
		for _, target := range []string{
			apiPath(pluginsLocal, tc.file), itemPath(pluginsLocal, tc.file),
		} {
			t.Run(tc.as.p.Name+" "+target, func(t *testing.T) {
				got := listed(t, w.do(http.MethodGet, target, tc.as))
				if !slices.Equal(got, tc.want) {
					t.Errorf("lists %v, want %v", got, tc.want)
				}
			})
		}
	}
	for _, target := range []string{
		apiPath(pluginsLocal, "vscode-marketplace.json"),
		itemPath(pluginsLocal, "vscode-marketplace.json"),
	} {
		t.Run("HEAD "+target, func(t *testing.T) {
			rec := w.do(http.MethodHead, target, admin)
			if rec.Code != http.StatusOK || rec.Body.Len() != 0 ||
				rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf(
					"answered %d %v with %d bytes, want 200 and the stored file's headers alone",
					rec.Code,
					rec.Header(),
					rec.Body.Len(),
				)
			}
		})
	}
}

// TestAccAgentPluginsVSCodeMarketplaceRefusesAsTheOthersDo: the VS Code
// file is refused as every marketplace file is: the read permission
// first, then the download policy at the file's own name (DV-057).
func TestAccAgentPluginsVSCodeMarketplaceRefusesAsTheOthersDo(t *testing.T) {
	w := newItemWorld(t)
	dark := pluginsRepo("plugins-dark", repo.Local)
	dark.BlackedOut = new(true)
	closed := pluginsRepo("plugins-closed", repo.Local)
	closed.ExcludesPattern = new("vscode-*.json")
	w.repos(pluginsRepo(pluginsLocal, repo.Local), dark, closed)
	w.standardPlugin("alpha", "1.0.0")
	for _, tc := range []struct {
		name    string
		key     string
		as      *caller
		status  int
		message string
	}{
		{
			"blacked-out repository", "plugins-dark", admin, http.StatusNotFound,
			"The repository 'plugins-dark' is blacked out and cannot serve artifact " +
				"'plugins-dark:vscode-marketplace.json'.",
		},
		{
			"excluded by the patterns", "plugins-closed", admin, http.StatusNotFound,
			"The repository 'plugins-closed' rejected the artifact " +
				"'plugins-closed:vscode-marketplace.json' due to its include/exclude pattern settings.",
		},
		{
			"excluded, to a caller who may not read it", "plugins-closed", user,
			http.StatusForbidden, "Forbidden",
		},
		{
			"anonymous user who may not read the repository", pluginsLocal, anonymous,
			http.StatusUnauthorized, "Authentication is required",
		},
	} {
		for _, target := range []string{
			apiPath(tc.key, "vscode-marketplace.json"), itemPath(tc.key, "vscode-marketplace.json"),
		} {
			t.Run(tc.name+" at "+target, func(t *testing.T) {
				wantError(t, w.do(http.MethodGet, target, tc.as), tc.status, tc.message)
			})
		}
	}
}
