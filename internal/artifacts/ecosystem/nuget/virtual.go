package nuget

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Contribution keeps a version's complete supplying record together. The adapter
// owns Value: a remote catalog entry must not lose fields when versions merge.
type Contribution[T any] struct {
	Version string
	Path    string
	Value   T
}

// Package is one member's readable contribution for an id.
type Package[T any] struct {
	ID       string
	Versions []Contribution[T]
}

// Merger unions versions in NuGet order, with the first member owning each
// normalized version. A readable priority holder shadows non-priority members
// for the whole id, including versions that the virtual's policy then hides.
type Merger[T any] struct {
	Allowed func(string) bool
}

// Merge applies per-id priority and preserves the first whole version record.
func (m Merger[T]) Merge(ctx context.Context, parts []virtual.Part[Package[T]]) (Package[T], error) {
	out := Package[T]{}
	priority := slices.ContainsFunc(parts, func(p virtual.Part[Package[T]]) bool {
		return p.Member.PriorityResolution.Bool() && len(p.Doc.Versions) > 0
	})
	seen := map[string]bool{}
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return out, fmt.Errorf("nuget merge canceled: %w", err)
		}
		if priority && !part.Member.PriorityResolution.Bool() {
			continue
		}
		for _, v := range part.Doc.Versions {
			parsed, ok := ParseVersion(v.Version)
			if !ok || m.Allowed != nil && !m.Allowed(v.Path) {
				continue
			}
			key := VersionKey(parsed)
			if seen[key] {
				continue
			}
			seen[key] = true
			if out.ID == "" {
				out.ID = PackageName(part.Doc.ID)
			}
			v.Version = key
			out.Versions = append(out.Versions, v)
		}
	}
	slices.SortFunc(out.Versions, func(a, b Contribution[T]) int { return model.CompareNuGet(a.Version, b.Version) })
	return out, nil
}

// SearchMerger preserves first-seen id order across bounded member results.
// Each id retains Merger's whole-version ownership; its final version is the
// newest metadata owner. totalHits is the distinct ids this bounded merge saw.
// [I] Published 7.x sources do not specify cross-member search rank or totals.
type SearchMerger[T any] struct{ Merger[T] }

// Merge combines bounded search hits before the adapter applies client paging.
func (m SearchMerger[T]) Merge(ctx context.Context, parts []virtual.Part[[]Package[T]]) ([]Package[T], error) {
	order := []string{}
	byID := map[string][]virtual.Part[Package[T]]{}
	for _, part := range parts {
		for _, p := range part.Doc {
			id := strings.ToLower(p.ID)
			if _, ok := byID[id]; !ok {
				order = append(order, id)
			}
			byID[id] = append(byID[id], virtual.Part[Package[T]]{Member: part.Member, Doc: p})
		}
	}
	out := make([]Package[T], 0, len(order))
	for _, id := range order {
		p, err := m.Merger.Merge(ctx, byID[id])
		if err != nil {
			return nil, err
		}
		if len(p.Versions) > 0 {
			out = append(out, p)
		}
	}
	return out, nil
}
