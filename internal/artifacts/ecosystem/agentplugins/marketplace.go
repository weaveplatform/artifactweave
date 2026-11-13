package agentplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var indexes = map[string]string{
	"marketplace.json":        "",
	"claude-marketplace.json": "claude",
	"cursor-marketplace.json": "cursor",
	"codex-marketplace.json":  "codex",
	"vscode-marketplace.json": "vscode",
}

// IsMarketplace reports whether name is one of the marketplace files a local
// Agent Plugins repository keeps at its root: marketplace.json, which lists
// every plugin; claude-marketplace.json and codex-marketplace.json, which
// list the plugins carrying that harness's manifest;
// cursor-marketplace.json, which lists those with a Cursor manifest or a
// conforming root plugin.json; and vscode-marketplace.json, which lists
// those with a conforming root plugin.json only (the Agent Plugins
// Repositories docs' "Marketplace Files and Indexing" table, 7.173.8).
func IsMarketplace(name string) bool { _, ok := indexes[name]; return ok }

// marketplaces returns the content of every marketplace file of the
// repository, by name. Each file lists, for each plugin, the newest version
// of a layout it names. The files are stored, so they list every plugin
// whoever reads them: 7.x keeps them at the repository root as files of
// their own, which any caller who may read that path downloads as they
// are.
func marketplaces(ctx context.Context, st format.Store, repoKey string) (map[string][]byte, error) {
	lists := make(map[string][]map[string]any, len(indexes))
	for name := range indexes {
		lists[name] = make([]map[string]any, 0)
	}
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		pkgs, next, err := st.Packages().List(ctx, repoKey, page)
		if err != nil {
			return nil, fmt.Errorf("agentplugins: list packages: %w", err)
		}
		for _, p := range pkgs {
			if err := listPlugin(ctx, st, p, lists); err != nil {
				return nil, err
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	out := make(map[string][]byte, len(lists))
	for name, plugins := range lists {
		data, err := json.Marshal(map[string]any{
			"name":    repoKey,
			"owner":   map[string]string{"name": repoKey},
			"plugins": plugins,
		})
		if err != nil {
			return nil, fmt.Errorf("agentplugins: marshal %s: %w", name, err)
		}
		out[name] = data
	}
	return out, nil
}

// listPlugin appends to each file's list the entry of package p's newest
// version that the file lists, reading the versions from the newest down
// until every file has its entry.
func listPlugin(
	ctx context.Context,
	st format.Store,
	p model.Package,
	lists map[string][]map[string]any,
) error {
	versions, err := st.Packages().Versions(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("agentplugins: versions of %s: %w", p.Name, err)
	}
	model.SortVersions(model.ComparatorFor(p.Type), versions)
	done := make(map[string]bool, len(indexes))
	for _, v := range slices.Backward(versions) {
		var meta Metadata
		if err := json.Unmarshal(v.Metadata, &meta); err != nil {
			return fmt.Errorf("agentplugins: metadata of %s %s: %w", p.Name, v.Version, err)
		}
		for name, harness := range indexes {
			if done[name] || !meta.listedIn(harness) {
				continue
			}
			entry, err := marketplaceEntry(meta, harness)
			if err != nil {
				return err
			}
			lists[name] = append(lists[name], entry)
			done[name] = true
		}
		if len(done) == len(indexes) {
			break
		}
	}
	return nil
}

// listedIn reports whether the marketplace file of harness lists this
// version: the unified file lists every version, the VS Code file only the
// standard layout, the Cursor file a Cursor manifest or the standard
// layout, and the others their own harness's manifest.
func (m Metadata) listedIn(harness string) bool {
	switch harness {
	case "":
		return true
	case "vscode":
		return len(m.Standard) > 0
	case "cursor":
		return len(m.Standard) > 0 || slices.Contains(m.Harnesses, harness)
	default:
		return slices.Contains(m.Harnesses, harness)
	}
}

// manifestFor picks the manifest whose fields fill the entry, and reports
// whether it is the root plugin.json. A harness's own manifest comes
// first: the docs have the Cursor file use .cursor-plugin/plugin.json when
// a bundle carries it beside a conforming root plugin.json, and the
// unified file keeps, for a bundle with both layouts, the first of the
// Claude, Cursor and Codex manifests that it used before the standard was
// indexed [I]. The root plugin.json fills the rest.
func (m Metadata) manifestFor(harness string) (json.RawMessage, bool) {
	switch harness {
	case "vscode":
		return m.Standard, true
	case "":
		for _, h := range []string{"claude", "cursor", "codex"} {
			if slices.Contains(m.Harnesses, h) {
				return m.Manifests[h], false
			}
		}
		return m.Standard, true
	case "cursor":
		if !slices.Contains(m.Harnesses, harness) {
			return m.Standard, true
		}
	}
	return m.Manifests[harness], false
}

func marketplaceEntry(meta Metadata, harness string) (map[string]any, error) {
	entry := map[string]any{}
	raw, standard := meta.manifestFor(harness)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("agentplugins: manifest of %s: %w", meta.Name, err)
		}
	}
	// The root plugin.json's $schema names a closed schema with no source
	// field, so an entry that kept it would claim a schema it fails [I].
	// Harness manifests keep any $schema of their own, as they always have.
	if standard {
		delete(entry, "$schema")
	}
	entry["name"] = meta.Name
	entry["version"] = meta.Version
	kind := "zip"
	if harness == "claude" {
		kind = "url"
	}
	source := map[string]string{"source": kind, "url": meta.Path}
	if kind == "url" && meta.SHA1 != "" {
		source["sha"] = meta.SHA1
	}
	entry["source"] = source
	return entry, nil
}
