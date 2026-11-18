package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// SearchHit retains one complete eligible search object and its package name.
// Version values remain opaque: a nonstandard version must not break npm search.
type SearchHit[T any] struct {
	Name  string
	Value T
}

// SearchMerger concatenates effective member results, retaining the first whole
// object for a name, then pages that union. Inputs are filtered before merging.
// [I] Public 7.x sources do not settle native-search name ownership (#216).
type SearchMerger[T any] struct{ From, Size int }

// Merge applies paging without allocating from client-controlled page sizes or
// adding from+size, which can overflow for otherwise valid query integers.
func (m SearchMerger[T]) Merge(ctx context.Context, parts []virtual.Part[[]SearchHit[T]]) ([]SearchHit[T], error) {
	out := []SearchHit[T]{}
	seen := map[string]bool{}
	skip := max(0, m.From)
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("npm search merge: %w", err)
		}
		for _, hit := range part.Doc {
			if seen[hit.Name] {
				continue
			}
			seen[hit.Name] = true
			if skip > 0 {
				skip--
				continue
			}
			if len(out) >= m.Size {
				return out, nil
			}
			out = append(out, hit)
		}
	}
	return out, nil
}

// SafeSearchVersion permits non-SemVer labels while refusing separators,
// encoded path syntax and control characters in the canonical permission path.
// It does not make an unsafe upstream object admissible merely because search
// does not need to interpret its version (RTFACT-30321).
func SafeSearchVersion(version string) bool {
	return version != "" && !strings.ContainsAny(version, "/\\;?#%") && !strings.ContainsFunc(version, unicode.IsControl)
}

// Keywords is a package.json keywords field as indexing reads it into
// npm.keywords: one string split at commas, and entries that are no
// non-empty string dropped. npm search matches against these, so a keyword
// found by the property search is found by npm search too.
func Keywords(raw json.RawMessage) []string { return keywords(raw) }
