package agentplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var indexes = map[string]string{
	"marketplace.json":        "",
	"claude-marketplace.json": "claude",
	"cursor-marketplace.json": "cursor",
	"codex-marketplace.json":  "codex",
}

var ErrMarketplace = errors.New("agentplugins: unknown agent plugin marketplace")

func IsMarketplace(name string) bool { _, ok := indexes[name]; return ok }

func IsReservedMarketplace(name string) bool {
	return IsMarketplace(name) || name == "vscode-marketplace.json"
}

// Marketplace selects each package's latest readable version. Rendering for
// each caller avoids exposing ZIPs hidden by path-scoped Read permissions.
func Marketplace(
	ctx context.Context,
	st store.Store,
	repoKey, name string,
	can func(string) bool,
) ([]byte, error) {
	harness, ok := indexes[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrMarketplace, name)
	}
	plugins := make([]map[string]any, 0)
	page := store.Page{Limit: store.MaxLimit}
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
			types.SortVersions(types.ComparatorFor(p.Type), versions)
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
				if !can(meta.Path) || harness != "" && !slices.Contains(meta.Harnesses, harness) {
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

func marketplaceEntry(meta Metadata, harness string) (map[string]any, error) {
	manifestHarness := harness
	if manifestHarness == "" {
		for _, h := range []string{"claude", "cursor", "codex"} {
			if slices.Contains(meta.Harnesses, h) {
				manifestHarness = h
				break
			}
		}
	}
	entry := map[string]any{}
	if raw := meta.Manifests[manifestHarness]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("agentplugins: manifest of %s: %w", meta.Name, err)
		}
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
