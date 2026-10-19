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

// Listed returns the lines @v/list answers for a module's visible
// versions, in ascending order: each version, pseudo-versions included,
// with its ListTime in RFC 3339. Artifactory dates each line from the
// version's .info since 7.8.1 (RTFACT-21670). The go command reads the
// time only to choose @latest among pseudo-versions when @latest itself
// fails, and leaves pseudo-versions out of go list -m -versions whatever
// the list says, so listing them changes no version it picks.
func Listed(versions []types.PackageVersion) []string {
	sorted := slices.Clone(versions)
	slices.SortFunc(
		sorted,
		func(a, b types.PackageVersion) int { return compare(a.Version, b.Version) },
	)
	out := make([]string, 0, len(sorted))
	for _, v := range sorted {
		out = append(out, v.Version+" "+ListTime(v).UTC().Format(time.RFC3339))
	}
	return out
}

// Latest returns the version @latest answers among a module's visible
// versions, chosen as the go command prefers: the highest release; when
// there is none, the highest pre-release that is not a pseudo-version; when
// there is none, the pseudo-version with the latest time it encodes. ok is
// false when versions is empty. The go command asks @latest only when it
// finds no release or pre-release in @v/list, so the last rule is what
// resolves a module published only at pseudo-versions.
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
