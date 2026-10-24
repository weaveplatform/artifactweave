package agentplugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitMarketplaceLatestAndPermissionFiltering(t *testing.T) {
	ctx := context.Background()
	st := inmem.New()
	p, err := st.Packages().
		Ensure(ctx, types.Package{RepoKey: "plugins", Type: repo.AgentPlugins, Name: "reader", Created: time.Now()})
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
			PutVersion(ctx, types.PackageVersion{PackageID: p.ID, Version: version, Metadata: b}); err != nil {
			t.Fatal(err)
		}
	}
	put("1.9.0", []string{"claude"})
	put("1.10.0", []string{"claude", "cursor"})
	decode := func(name string, can func(string) bool) map[string]any {
		t.Helper()
		b, err := Marketplace(ctx, st, "plugins", name, can)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	entry := func(out map[string]any) map[string]any {
		t.Helper()
		plugins := out["plugins"].([]any)
		if len(plugins) != 1 {
			t.Fatalf("plugins: %+v", plugins)
		}
		return plugins[0].(map[string]any)
	}
	latest := entry(decode("claude-marketplace.json", func(string) bool { return true }))
	if latest["version"] != "1.10.0" || latest["name"] != "reader" ||
		latest["description"] != "safe" {
		t.Fatalf("latest: %+v", latest)
	}
	source := latest["source"].(map[string]any)
	if source["source"] != "url" || source["url"] != Path("reader", "1.10.0") ||
		source["sha"] != strings.Repeat("a", 40) {
		t.Fatalf("Claude source: %+v", source)
	}
	if unified := entry(
		decode("marketplace.json", func(string) bool { return true }),
	); unified["source"].(map[string]any)["source"] != "zip" {
		t.Fatalf("unified source: %+v", unified)
	}
	older := entry(
		decode(
			"claude-marketplace.json",
			func(p string) bool { return p == Path("reader", "1.9.0") },
		),
	)
	if older["version"] != "1.9.0" {
		t.Fatalf("visible older: %+v", older)
	}
	if plugins := decode("cursor-marketplace.json", func(p string) bool { return p == Path("reader", "1.9.0") })["plugins"].([]any); len(
		plugins,
	) != 0 {
		t.Fatalf("leaked denied cursor: %+v", plugins)
	}
	cursor := entry(decode("cursor-marketplace.json", func(string) bool { return true }))
	if cursor["source"].(map[string]any)["source"] != "zip" {
		t.Fatalf("Cursor source: %+v", cursor)
	}
	if _, err := Marketplace(
		ctx,
		st,
		"plugins",
		"vscode-marketplace.json",
		func(string) bool { return true },
	); err == nil {
		t.Fatal("accepted unsupported target-version index")
	}
	if _, err := marketplaceEntry(Metadata{Name: "bad", Manifests: map[string]json.RawMessage{
		"claude": []byte("{"),
	}}, "claude"); err == nil {
		t.Fatal("accepted malformed stored manifest")
	}
}
