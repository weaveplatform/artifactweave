package helm

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Merger builds a virtual index from canonical member-relative chart URLs.
// The HTTP walk decides member visibility; a readable member contributes every
// chart, including charts whose own download permission refuses the caller.
type Merger struct {
	Settings  Settings
	Base, Key string
}

// Merge retains the first member's copy of a chart version, sorts newest first,
// and points all retained chart URLs back through the requested virtual.
func (m Merger) Merge(_ context.Context, parts []virtual.Part[Index]) (Index, error) {
	if len(parts) == 0 {
		return Index{}, kernel.ErrNotFound
	}
	out := Index{APIVersion: "v1", Entries: map[string][]ChartVersion{}, Generated: "0001-01-01T00:00:00Z"}
	seen := map[string]map[string]bool{}
	for _, part := range parts {
		if stamp, err := time.Parse(time.RFC3339Nano, part.Doc.Generated); err == nil {
			previous, _ := time.Parse(time.RFC3339Nano, out.Generated)
			if stamp.After(previous) {
				out.Generated = part.Doc.Generated
			}
		}
		for name, versions := range part.Doc.Entries {
			if seen[name] == nil {
				seen[name] = map[string]bool{}
			}
			for _, version := range versions {
				if seen[name][version.Version] {
					continue
				}
				var locations []string
				for _, location := range version.URLs {
					u, err := url.Parse(location)
					if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || strings.HasPrefix(u.Path, "_external/") {
						continue
					}
					p := strings.TrimPrefix(u.Path, "/")
					if p == "" {
						continue
					}
					location = chartURL(p)
					if !m.Settings.IsRelative() {
						location = virtual.ItemBase(m.Base, m.Key) + "/" + location
					}
					locations = append(locations, location)
				}
				if len(locations) == 0 {
					continue
				}
				seen[name][version.Version] = true
				version.URLs = locations
				version.Annotations = maps.Clone(version.Annotations)
				out.Entries[name] = append(out.Entries[name], version)
			}
		}
	}
	for _, versions := range out.Entries {
		slices.SortStableFunc(versions, func(a, b ChartVersion) int { return model.CompareSemver(b.Version, a.Version) })
	}
	return out, nil
}
