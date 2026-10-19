package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TagLatest is the dist-tag npm installs when no version is asked for. 7.x
// stores it as it stores any dist-tag, as npm.disttag=latest on one
// tarball (7.84.10, RTDEV-37593 and RTDEV-40221; RTFACT-31340), so it moves
// only when one of decision 0034's rules moves it:
//  1. npm.disttag=latest set on a tarball by any route makes its version
//     latest (Annotated, through moveTags);
//  2. a new version written without a dist-tag takes latest when it
//     outranks the version that has it (claimLatest);
//  3. a new version written with a dist-tag leaves latest where it is,
//     unless no version has it yet (claimLatest; RTFACT-24072);
//  4. a package left without latest, its version removed or the tag taken
//     off, gives it to the version that outranks the others (settleLatest).
const TagLatest = "latest"

// Outranks reports whether version a outranks b for latest: a version that
// is no pre-release outranks every pre-release, and between two of the same
// kind the higher SemVer wins, as a 7.x local repository ranks them
// (RTFACT-30783, RTFACT-17732). Both are versions as the index cleans them,
// without build metadata, so a hyphen can only start a pre-release.
func Outranks(a, b string) bool {
	if pa, pb := prerelease(a), prerelease(b); pa != pb {
		return pb
	}
	return types.CompareSemver(a, b) > 0
}

func prerelease(v string) bool { return strings.Contains(v, "-") }

// Highest is the version of versions that outranks every other, "" when
// there is none.
func Highest(versions []string) string {
	var top string
	for _, v := range versions {
		if top == "" || Outranks(v, top) {
			top = v
		}
	}
	return top
}

// tagsOf reads only the dist-tags of a version's Metadata, which is all a
// question about latest needs of every version of a package.
func tagsOf(meta []byte) ([]string, error) {
	var m struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		return nil, fmt.Errorf("npm: version metadata: %w", err)
	}
	return m.Tags, nil
}

// holderOf is the version of versions whose tarball carries latest. Only a
// race leaves two, and then the one that outranks the other is latest, as
// a read takes it.
func holderOf(versions []types.PackageVersion) (types.PackageVersion, bool, error) {
	var (
		holder types.PackageVersion
		found  bool
	)
	for _, v := range versions {
		tags, err := tagsOf(v.Metadata)
		if err != nil {
			return holder, false, err
		}
		if slices.Contains(tags, TagLatest) && (!found || Outranks(v.Version, holder.Version)) {
			holder, found = v, true
		}
	}
	return holder, found, nil
}

// claimLatest applies rules 2 and 3 to pv, the version a write just indexed
// from a tarball carrying the dist-tags tags. A tarball that carries latest
// itself is rule 1's, which Annotated applies once the write's properties
// are set. A version that was indexed before the write, a redeploy, keeps
// latest where it is, so a redeploy undoes no npm dist-tag add; only a
// package left without latest gives it out again.
func claimLatest(
	ctx context.Context,
	st store.Store,
	pv types.PackageVersion,
	tags []string,
	isNew bool,
	now time.Time,
) error {
	if slices.Contains(tags, TagLatest) {
		return nil
	}
	versions, err := st.Packages().Versions(ctx, pv.PackageID)
	if err != nil {
		return fmt.Errorf("npm: versions of package %d: %w", pv.PackageID, err)
	}
	holder, ok, err := holderOf(versions)
	switch {
	case err != nil:
		return err
	case !ok:
		return giveLatest(ctx, st, versions, now)
	case !isNew || len(tags) > 0 || !Outranks(pv.Version, holder.Version):
		return nil
	}
	return tagLatest(ctx, st, pv, now)
}

// settleLatest applies rule 4 to the package with id: when no version
// carries latest, the version that outranks the others takes it. A package
// with no version left has nothing to settle.
func settleLatest(ctx context.Context, st store.Store, id int64, now time.Time) error {
	versions, err := st.Packages().Versions(ctx, id)
	if err != nil {
		return fmt.Errorf("npm: versions of package %d: %w", id, err)
	}
	if _, ok, err := holderOf(versions); err != nil || ok {
		return err
	}
	return giveLatest(ctx, st, versions, now)
}

// giveLatest tags the version of versions that outranks the others latest.
// [I] Whether 7.x then prefers the most recent deploy among pre-releases
// alone is unknown (#216).
func giveLatest(
	ctx context.Context,
	st store.Store,
	versions []types.PackageVersion,
	now time.Time,
) error {
	if len(versions) == 0 {
		return nil
	}
	numbers := make([]string, 0, len(versions))
	for _, v := range versions {
		numbers = append(numbers, v.Version)
	}
	top := Highest(numbers)
	i := slices.IndexFunc(versions, func(v types.PackageVersion) bool { return v.Version == top })
	return tagLatest(ctx, st, versions[i], now)
}

// tagLatest adds latest to the dist-tags of pv's tarball, takes it from
// the package's other versions and brings pv's Metadata in line, at now.
func tagLatest(ctx context.Context, st store.Store, pv types.PackageVersion, now time.Time) error {
	files, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return fmt.Errorf("npm: files of version %s: %w", pv.Version, err)
	}
	for _, id := range files {
		props, err := st.Items().Properties(ctx, id)
		if err != nil {
			return fmt.Errorf("npm: properties of item %d: %w", id, err)
		}
		tags := append(nonEmpty(props[PropDistTag]), TagLatest)
		if err := st.Items().PatchProperties(ctx, id,
			types.Properties{PropDistTag: tags}, nil, now); err != nil {
			return fmt.Errorf("npm: dist-tags of item %d: %w", id, err)
		}
		if err := refresh(ctx, st, pv, id); err != nil {
			return err
		}
	}
	return moveTags(ctx, st, pv, []string{TagLatest}, now)
}
