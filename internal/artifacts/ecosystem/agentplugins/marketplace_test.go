package agentplugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// TestUnitMarketplacesListEachPluginsNewestVersion: every marketplace file
// lists, for each plugin, the newest version of a layout it names, whoever
// will read it, since 7.x stores one file for every reader. Claude's file
// advertises the ZIP's URL with its SHA-1, the others a ZIP source, and
// the path names the plugin and its version, whatever the manifest says.
func TestUnitMarketplacesListEachPluginsNewestVersion(t *testing.T) {
	ctx := context.Background()
	st := inmem.New()
	p, err := st.Packages().
		Ensure(ctx, model.Package{RepoKey: "plugins", Type: repo.AgentPlugins, Name: "reader", Created: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	put := func(version string, harnesses []string) {
		t.Helper()
		m := Metadata{
			Name:      "reader",
			Version:   version,
			Path:      Path("reader", version),
			SHA1:      strings.Repeat("a", 40),
			Harnesses: harnesses,
			Manifests: map[string]json.RawMessage{
				"claude": []byte(
					`{"name":"manifest-lie","version":"999.0.0","description":"safe"}`,
				),
			},
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Packages().
			PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: version, Metadata: b}); err != nil {
			t.Fatal(err)
		}
	}
	put("1.9.0", []string{"claude", "cursor"})
	put("1.10.0", []string{"claude"})
	// Another plugin whose one version every file lists.
	every, err := st.Packages().
		Ensure(ctx, model.Package{RepoKey: "plugins", Type: repo.AgentPlugins, Name: "every", Created: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(Metadata{
		Name: "every", Version: "1.0.0", Path: Path("every", "1.0.0"),
		Harnesses: []string{"claude", "codex", "cursor"},
		Standard:  json.RawMessage(`{"name":"every"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Packages().
		PutVersion(ctx, model.PackageVersion{PackageID: every.ID, Version: "1.0.0", Metadata: b}); err != nil {
		t.Fatal(err)
	}
	files, err := marketplaces(ctx, st, "plugins")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(indexes) {
		t.Fatalf("built %d files, want one for each of the %d names", len(files), len(indexes))
	}
	decode := func(name string) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(files[name], &out); err != nil {
			t.Fatal(err)
		}
		if out["name"] != "plugins" || out["owner"].(map[string]any)["name"] != "plugins" {
			t.Errorf(
				"%s named %v, owned by %v, want plugins for both",
				name,
				out["name"],
				out["owner"],
			)
		}
		return out
	}
	// entry is the reader plugin's entry in the file name, which lists the
	// plugins in name order, after every plugin's.
	entry := func(name string) map[string]any {
		t.Helper()
		plugins := decode(name)["plugins"].([]any)
		if len(plugins) != 2 || plugins[0].(map[string]any)["name"] != "every" {
			t.Fatalf("%s plugins: %+v", name, plugins)
		}
		return plugins[1].(map[string]any)
	}
	latest := entry("claude-marketplace.json")
	if latest["version"] != "1.10.0" || latest["name"] != "reader" ||
		latest["description"] != "safe" {
		t.Fatalf("latest: %+v", latest)
	}
	source := latest["source"].(map[string]any)
	if source["source"] != "url" || source["url"] != Path("reader", "1.10.0") ||
		source["sha"] != strings.Repeat("a", 40) {
		t.Fatalf("Claude source: %+v", source)
	}
	if unified := entry("marketplace.json"); unified["version"] != "1.10.0" ||
		unified["source"].(map[string]any)["source"] != "zip" {
		t.Fatalf("unified: %+v", unified)
	}
	// 1.10.0 dropped the Cursor manifest, so the Cursor file keeps 1.9.0.
	cursor := entry("cursor-marketplace.json")
	if cursor["version"] != "1.9.0" || cursor["source"].(map[string]any)["source"] != "zip" {
		t.Fatalf("Cursor: %+v", cursor)
	}
	for _, name := range []string{"codex-marketplace.json", "vscode-marketplace.json"} {
		if plugins := decode(name)["plugins"].([]any); len(plugins) != 1 ||
			plugins[0].(map[string]any)["name"] != "every" {
			t.Errorf("%s lists %+v, want every alone", name, plugins)
		}
	}
	if _, err := marketplaceEntry(Metadata{Name: "bad", Manifests: map[string]json.RawMessage{
		"claude": []byte("{"),
	}}, "claude"); err == nil {
		t.Fatal("accepted malformed stored manifest")
	}
}

// TestUnitMarketplacesFailOnWhatTheyCannotRead: a package listing, a
// version listing, a version's metadata or a listed manifest that cannot
// be read fails the build, naming the step, rather than storing files that
// leave a plugin out.
func TestUnitMarketplacesFailOnWhatTheyCannotRead(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, metadata string) *inmem.Store {
		t.Helper()
		st := inmem.New()
		p, err := st.Packages().Ensure(ctx, model.Package{
			RepoKey: "plugins", Type: repo.AgentPlugins, Name: "reader", Created: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Packages().PutVersion(ctx, model.PackageVersion{
			PackageID: p.ID, Version: "1.0.0", Metadata: []byte(metadata),
		}); err != nil {
			t.Fatal(err)
		}
		return st
	}
	for _, tc := range []struct {
		name string
		st   func(t *testing.T) format.Store
		want string
	}{
		{
			"package listing",
			func(t *testing.T) format.Store { return listingFails{seed(t, "{}")} },
			"agentplugins: list packages",
		},
		{
			"version listing",
			func(t *testing.T) format.Store { return versionsFail{seed(t, "{}")} },
			"agentplugins: versions of reader",
		},
		{
			"metadata",
			func(t *testing.T) format.Store { return seed(t, "{") },
			"agentplugins: metadata of reader 1.0.0",
		},
		{
			"manifest",
			func(t *testing.T) format.Store {
				return seed(t, `{"name":"reader","harnesses":["claude"],"manifests":{"claude":"x"}}`)
			},
			"agentplugins: manifest of reader",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := marketplaces(ctx, tc.st(t), "plugins")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("built with %v, want an error naming %q", err, tc.want)
			}
		})
	}
}
