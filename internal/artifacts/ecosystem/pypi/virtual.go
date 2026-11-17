package pypi

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Distribution retains a member's representation intact while the merger
// compares names and applies the virtual's path patterns.
type Distribution[T any] struct {
	Name, Path string
	Value      T
	Info       string
	Uploaded   time.Time
}

// Release is one member's visible files and the core text used for Warehouse
// info. The first member contributing that version owns its info.
type Release[T any] struct {
	Version string
	Files   []Distribution[T]
	Info    string
	Owner   T
}

// Project is a member's readable simple-index model.
type Project[T any] struct {
	Name     string
	Releases []Release[T]
}

// Merger keeps filename/hash ownership together. Member permissions and page
// policy have already been checked; Allowed applies only the virtual's patterns.
type Merger[T any] struct{ Allowed func(path string) bool }

// Merge selects priority projects when present, then retains the first filename.
func (m Merger[T]) Merge(_ context.Context, parts []virtual.Part[Project[T]]) (Project[T], error) {
	priority := slices.ContainsFunc(parts, func(p virtual.Part[Project[T]]) bool {
		return p.Member.PriorityResolution.Bool() && hasFiles(p.Doc)
	})
	out := Project[T]{}
	versions := map[string]int{}
	type owner struct {
		part int
		at   time.Time
		path string
	}
	owners := map[string]owner{}
	seen := map[string]bool{}
	for partIndex, part := range parts {
		if priority && !part.Member.PriorityResolution.Bool() {
			continue
		}
		if out.Name == "" {
			out.Name = part.Doc.Name
		}
		for _, rel := range part.Doc.Releases {
			version := NormalizeVersion(rel.Version)
			for _, f := range rel.Files {
				if seen[f.Name] || m.Allowed != nil && !m.Allowed(f.Path) {
					continue
				}
				seen[f.Name] = true
				i, ok := versions[version]
				if !ok {
					i = len(out.Releases)
					versions[version] = i
					out.Releases = append(out.Releases, Release[T]{Version: rel.Version, Info: f.Info, Owner: f.Value})
					owners[version] = owner{part: partIndex, at: f.Uploaded, path: f.Path}
				}
				current := owners[version]
				if current.part == partIndex && (f.Uploaded.Before(current.at) || f.Uploaded.Equal(current.at) && f.Path < current.path) {
					out.Releases[i].Info, out.Releases[i].Owner = f.Info, f.Value
					owners[version] = owner{part: partIndex, at: f.Uploaded, path: f.Path}
				}
				out.Releases[i].Files = append(out.Releases[i].Files, f)
			}
		}
	}
	slices.SortStableFunc(out.Releases, func(a, b Release[T]) int { return model.ComparePEP440(a.Version, b.Version) })
	for _, rel := range out.Releases {
		slices.SortFunc(rel.Files, func(a, b Distribution[T]) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Path, b.Path))
		})
	}
	return out, nil
}

func hasFiles[T any](p Project[T]) bool {
	return slices.ContainsFunc(p.Releases, func(r Release[T]) bool { return len(r.Files) > 0 })
}

// ListMerger returns the readable project union. Priority applies to a project's
// versions, never to whether an unrelated project's name is discoverable.
type ListMerger struct{}

// Merge normalizes and sorts the member project union.
func (ListMerger) Merge(_ context.Context, parts []virtual.Part[[]string]) ([]string, error) {
	seen := map[string]bool{}
	for _, part := range parts {
		for _, name := range part.Doc {
			seen[Normalize(name)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}
