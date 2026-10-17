package golang

import (
	"slices"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// order is the package index's order of Go versions (decision 0018):
// semantic versioning without build metadata, so +incompatible does not
// affect it. A module path admits either vN.x.y with its /vN suffix or
// vN.x.y+incompatible without one, never both, so no two versions of one
// module compare equal.
var order = types.ComparatorFor(repo.Go)

func compare(a, b string) int {
	return order.Compare(types.PackageVersion{Version: a}, types.PackageVersion{Version: b})
}

// Listed returns what @v/list answers for a module's visible versions:
// every one that is not a pseudo-version, in ascending order, as the
// GOPROXY protocol asks.
func Listed(versions []string) []string {
	out := make([]string, 0, len(versions))
	for _, v := range versions {
		if !module.IsPseudoVersion(v) {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, compare)
	return out
}

// Latest returns the version @latest answers among a module's visible
// versions, chosen as the go command prefers: the highest release; when
// there is none, the highest pre-release that is not a pseudo-version; when
// there is none, the pseudo-version with the latest time it encodes. ok is
// false when versions is empty. @v/list leaves pseudo-versions out, so
// without the last rule a module published only at pseudo-versions could
// not be resolved.
func Latest(versions []string) (latest string, ok bool) {
	var release, pre, pseudo []string
	for _, v := range versions {
		switch {
		case module.IsPseudoVersion(v):
			pseudo = append(pseudo, v)
		case semver.Prerelease(v) != "":
			pre = append(pre, v)
		default:
			release = append(release, v)
		}
	}
	switch {
	case len(release) > 0:
		return slices.MaxFunc(release, compare), true
	case len(pre) > 0:
		return slices.MaxFunc(pre, compare), true
	case len(pseudo) > 0:
		return slices.MaxFunc(pseudo, func(a, b string) int {
			if c := pseudoTime(a).Compare(pseudoTime(b)); c != 0 {
				return c
			}
			return compare(a, b)
		}), true
	}
	return "", false
}

// pseudoTime is the commit time a pseudo-version encodes. One whose
// timestamp is no valid time, such as month 13, sorts as the earliest.
func pseudoTime(v string) time.Time {
	t, err := module.PseudoVersionTime(v)
	if err != nil {
		return time.Time{}
	}
	return t
}
