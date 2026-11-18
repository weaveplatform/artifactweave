package terraform

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// ModuleVersion retains the complete supplying member's contribution. The
// caller removes unreadable or policy-rejected archives before merging.
type ModuleVersion[T any] struct {
	Version string
	Value   T
}

// ModuleMerger unions versions in the engine's member order. A duplicate
// retains its first eligible holder, including its bytes and source authority.
// [I] No primary 7.x source settles duplicate module-version ownership (#216).
type ModuleMerger[T any] struct{}

// Merge returns a sorted version union without changing its contributions.
func (ModuleMerger[T]) Merge(ctx context.Context, parts []virtual.Part[[]ModuleVersion[T]]) ([]ModuleVersion[T], error) {
	byVersion := map[string]ModuleVersion[T]{}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("terraform merge: %w", err)
	}
	for _, p := range parts {
		for _, v := range p.Doc {
			if _, exists := byVersion[v.Version]; !exists {
				byVersion[v.Version] = v
			}
		}
	}
	out := make([]ModuleVersion[T], 0, len(byVersion))
	for _, v := range byVersion {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b ModuleVersion[T]) int {
		if n := model.CompareSemver(a.Version, b.Version); n != 0 {
			return n
		}
		return strings.Compare(a.Version, b.Version)
	})
	return out, nil
}

var _ virtual.Merger[[]ModuleVersion[struct{}]] = ModuleMerger[struct{}]{}

// ProviderVersion retains one supplying member's whole protocol/platform record.
// Its version-union mechanics are shared with modules; ownership stays at the
// format boundary, never in the generic member walk.
type ProviderVersion[T any] = ModuleVersion[T]

// ProviderMerger keeps the first eligible whole version record in effective
// member order. Exact platform downloads resolve independently of this list.
// [I] Public 7.x sources do not settle duplicate provider ownership (#216).
type ProviderMerger[T any] struct{}

// Merge returns the sorted union without combining platforms from later owners.
func (ProviderMerger[T]) Merge(ctx context.Context, parts []virtual.Part[[]ProviderVersion[T]]) ([]ProviderVersion[T], error) {
	return (ModuleMerger[T]{}).Merge(ctx, parts)
}
