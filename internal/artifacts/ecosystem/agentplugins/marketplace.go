package agentplugins

import (
	"context"
	"encoding/json"
	"errors"
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

// ErrMarketplace is what Marketplace returns, wrapped with the name, for a
// name that is not a marketplace file.
var ErrMarketplace = errors.New("agentplugins: unknown agent plugin marketplace")

// IsMarketplace reports whether name is one of the marketplace files a local
// Agent Plugins repository renders at its root: marketplace.json, which
// lists every plugin; claude-marketplace.json and codex-marketplace.json,
// which list the plugins carrying that harness's manifest;
// cursor-marketplace.json, which lists those with a Cursor manifest or a
// conforming root plugin.json; and vscode-marketplace.json, which lists
// those with a conforming root plugin.json only (the Agent Plugins
// Repositories docs' "Marketplace Files and Indexing" table, 7.173.8).
func IsMarketplace(name string) bool { _, ok := indexes[name]; return ok }

// Marketplace selects each package's latest readable version. Rendering for
// each caller avoids exposing ZIPs hidden by path-scoped Read permissions.
func Marketplace(
	ctx context.Context,
	st format.Store,
	repoKey, name string,
	can func(string) bool,
) ([]byte, error) {
	harness, ok := indexes[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrMarketplace, name)
	}
	plugins := make([]map[string]any, 0)
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		pkgs, next, err := st.Packages().List(ctx, repoKey, page)
		if err != nil {
			return nil, fmt.Errorf("agentplugins: list packages: %w", err)
		}
		for _, p := range pkgs {
			versions, err := st.Packages().Versions(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("agentplugins: versions of %s: %w", p.Name, err)
			}
			model.SortVersions(model.ComparatorFor(p.Type), versions)
			for _, v := range slices.Backward(versions) {
				var meta Metadata
				if err := json.Unmarshal(v.Metadata, &meta); err != nil {
					return nil, fmt.Errorf(
						"agentplugins: metadata of %s %s: %w",
						p.Name,
						v.Version,
						err,
					)
				}
				if !can(meta.Path) || !meta.listedIn(harness) {
					continue
				}
				entry, err := marketplaceEntry(meta, harness)
				if err != nil {
					return nil, err
				}
				plugins = append(plugins, entry)
				break
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	out, err := json.Marshal(
		map[string]any{
			"name":    repoKey,
			"owner":   map[string]string{"name": repoKey},
			"plugins": plugins,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("agentplugins: marshal marketplace: %w", err)
	}
	return out, nil
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
