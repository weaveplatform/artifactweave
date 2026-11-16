package virtual

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Part is one member's share of a merged document: the real member a walk
// read it from, and what that member holds.
type Part[T any] struct {
	Member repo.Config
	Doc    T
}

// Merger is a format's merge of its members' metadata into the virtual
// repository's document. A format implements it in its
// ecosystem/<type>/virtual.go (decision 0008, "Per-format merges"); parts
// come in the walk's order (Members, Package), so a merge that keeps the
// first member's copy of a version keeps 7.x's.
type Merger[T any] interface {
	Merge(ctx context.Context, parts []Part[T]) (T, error)
}

// ErrPassOver is a member read that counts as not found: 7.x's walk passes
// over every member that does not serve the path, an unreadable one
// included, and a package handler answers its own not-found when none does
// (decision 0008, R3; RTDEV-88743, RTDEV-81046, RTFACT-30175). A read that
// returns it, or kernel.ErrNotFound, leaves its member out of Collect.
var ErrPassOver = errors.New("virtual: member passed over")

// Collect reads each of members in order with read and returns the parts
// that read, in that order. A member whose read is ErrPassOver or
// kernel.ErrNotFound is left out; any other error ends the walk and is
// returned, so a fault never shrinks a merge silently.
func Collect[T any](
	ctx context.Context,
	members []Member,
	read func(context.Context, Member) (T, error),
) ([]Part[T], error) {
	parts := make([]Part[T], 0, len(members))
	for _, m := range members {
		doc, err := read(ctx, m)
		switch {
		case errors.Is(err, ErrPassOver), errors.Is(err, kernel.ErrNotFound):
			continue
		case err != nil:
			return nil, fmt.Errorf("member %s: %w", m.Repo.Key, err)
		}
		parts = append(parts, Part[T]{Member: m.Repo, Doc: doc})
	}
	return parts, nil
}

// UnionBy is every item of parts, in order, with a key kept from the first
// part that has it: the first member in the walk wins a version that two
// members hold, as 7.x's merges keep the copy of the first member that
// serves it (decision 0008, "Per-format merges").
func UnionBy[T any, K comparable](parts [][]T, key func(T) K) []T {
	seen := map[K]bool{}
	var out []T
	for _, part := range parts {
		for _, it := range part {
			k := key(it)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, it)
		}
	}
	return out
}

// SortVersions sorts items by their version, as cmp orders two versions,
// keeping the walk's order for versions cmp counts equal, such as two
// builds of one SemVer version, so a merge's answer does not depend on the
// sort's algorithm.
func SortVersions[T any](items []T, version func(T) string, cmp func(a, b string) int) {
	slices.SortStableFunc(items, func(a, b T) int { return cmp(version(a), version(b)) })
}

// Latest is the greatest of versions, as cmp orders them, among those
// stable accepts; with none stable it is the greatest of all, and "" for
// none. 7.x picks a virtual repository's latest version stable first
// (research 25 section 7, KB 000007043), and falls back on a pre-release
// only when nothing else is left [I].
func Latest(versions []string, cmp func(a, b string) int, stable func(string) bool) string {
	var latest, latestStable string
	for _, v := range versions {
		if latest == "" || cmp(v, latest) > 0 {
			latest = v
		}
		if stable(v) && (latestStable == "" || cmp(v, latestStable) > 0) {
			latestStable = v
		}
	}
	if latestStable != "" {
		return latestStable
	}
	return latest
}

// Accepts reports whether a row at path, from the member m of the virtual
// repository v, belongs in a merged listing: both repositories' include and
// exclude patterns admit it. 7.x checks the virtual repository's patterns
// first, for administrators too, then the member's, and filters a merged
// listing by them before it is paged, so a page never counts a row it
// leaves out (decision 0008, R5; RTFACT-26524, RTFACT-29600,
// RTFACT-30802). folder is whether path names a folder, which the patterns
// admit when an include could match below it.
func Accepts(v, m repo.Config, path string, folder bool) bool {
	return repo.PatternsOf(v).AcceptsRequest(path, folder) &&
		repo.PatternsOf(m).AcceptsRequest(path, folder)
}

// APIBase is the URL of a native API on the virtual repository key, which
// a merged document points its files at so the client asks the virtual
// repository for them: <base>/artifactory/api/<api>/<key>. base is the
// request's base URL (baseurl.From), which honours
// X-JFrog-Override-Base-Url and never X-Artifactory-Override-Base-Url
// (DV-037).
func APIBase(base, api, key string) string {
	return strings.TrimRight(base, "/") + "/artifactory/api/" + api + "/" + key
}

// ItemBase is APIBase for a format whose files a client fetches from the
// item routes: <base>/artifactory/<key>.
func ItemBase(base, key string) string {
	return strings.TrimRight(base, "/") + "/artifactory/" + key
}
