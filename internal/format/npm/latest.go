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
//
// Which version outranks another is Settings.Outranks.
const TagLatest = "latest"

// Settings are the npm format's server-wide options (decision 0034), which
// reach it through formats.Settings. The zero value is 7.x's default.
type Settings struct {
	// TagLatestByPublish is npm.tag_latest_by_publish, 7.x's
	// artifactory.npm.tag.tagLatestByPublish: in rules 2 and 4 the version
	// deployed last outranks the others, pre-releases included, rather
	// than the highest. 7.x has it off by default (6.20's ConstantValues;
	// RTFACT-9754, RTFACT-20127).
	TagLatestByPublish bool
	// EnforcePath is npm.enforce_path, 7.x's artifactory.npm.enforce.path:
	// a package tarball deployed to a local repository under a file name
	// other than the one its package.json gives is refused (Admit). 7.x
	// has it off by default (npm-repositories).
	EnforcePath bool
}

// Candidate is a version as the latest rules weigh it.
type Candidate struct {
	// Version is the index's cleaned version.
	Version string
	// Published is when its tarball was last deployed (Metadata.Published).
	Published time.Time
}

// Outranks reports whether a outranks b for latest: by version (the
// package-level Outranks), or with TagLatestByPublish by deploy time, the
// later first. Two versions deployed at one moment fall back to the
// version order, so the choice never depends on the order they are read
// in. [I] How 7.x breaks that tie is unknown (#216).
func (s Settings) Outranks(a, b Candidate) bool {
	if s.TagLatestByPublish && !a.Published.Equal(b.Published) {
		return a.Published.After(b.Published)
	}
	return Outranks(a.Version, b.Version)
}

// Latest is the version of cs that outranks every other, "" when cs is
// empty.
func (s Settings) Latest(cs []Candidate) string {
	var top Candidate
	for i, c := range cs {
		if i == 0 || s.Outranks(c, top) {
			top = c
		}
	}
	return top.Version
}

// Outranks reports whether version a outranks b for latest by default: a
// version that is no pre-release outranks every pre-release, and between
// two of the same kind the higher SemVer wins, as a 7.x local repository
// ranks them (RTFACT-30783, RTFACT-17732). Both are versions as the index
// cleans them, without build metadata, so a hyphen can only start a
// pre-release.
func Outranks(a, b string) bool {
	if pa, pb := prerelease(a), prerelease(b); pa != pb {
		return pb
	}
	return types.CompareSemver(a, b) > 0
}

func prerelease(v string) bool { return strings.Contains(v, "-") }

// candidateOf reads what the latest rules need of a version's Metadata:
// its dist-tags, and when it was deployed. It decodes no more, since a
// question about latest reads every version of a package.
func candidateOf(v types.PackageVersion) (Candidate, []string, error) {
	var m struct {
		Tags      []string  `json:"tags"`
		Published time.Time `json:"published"`
	}
	if err := json.Unmarshal(v.Metadata, &m); err != nil {
		return Candidate{}, nil, fmt.Errorf("npm: version metadata: %w", err)
	}
	return Candidate{Version: v.Version, Published: m.Published}, m.Tags, nil
}

// holderOf is the version of versions whose tarball carries latest, with
// every version as a Candidate. Only a race leaves two holders, and then
// the one that outranks the other is latest, as a read takes it.
func (s Settings) holderOf(
	versions []types.PackageVersion,
) (holder types.PackageVersion, found bool, cs []Candidate, err error) {
	cs = make([]Candidate, 0, len(versions))
	var top Candidate
	for _, v := range versions {
		c, tags, err := candidateOf(v)
		if err != nil {
			return holder, false, nil, err
		}
		cs = append(cs, c)
		if slices.Contains(tags, TagLatest) && (!found || s.Outranks(c, top)) {
			holder, top, found = v, c, true
		}
	}
	return holder, found, cs, nil
}

// claimLatest applies rules 2 and 3 to pv, the version a write just indexed
// from a tarball carrying the dist-tags tags. A tarball that carries latest
// itself is rule 1's, which Annotated applies once the write's properties
// are set. A version that was indexed before the write, a redeploy, keeps
// latest where it is, so a redeploy of the same content undoes no npm
// dist-tag add; only a package left without latest gives it out again, as
// one does when a redeploy of other content took the tags off its file.
func (s Settings) claimLatest(
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
	holder, ok, cs, err := s.holderOf(versions)
	switch {
	case err != nil:
		return err
	case !ok:
		return s.giveLatest(ctx, st, versions, cs, now)
	case !isNew || len(tags) > 0:
		return nil
	}
	// Both decode: holderOf has read every version's Metadata.
	mine, _, _ := candidateOf(pv)
	theirs, _, _ := candidateOf(holder)
	if !s.Outranks(mine, theirs) {
		return nil
	}
	return tagLatest(ctx, st, pv, now)
}

// settleLatest applies rule 4 to the package with id: when no version
// carries latest, the version that outranks the others takes it. A package
// with no version left has nothing to settle.
func (s Settings) settleLatest(ctx context.Context, st store.Store, id int64, now time.Time) error {
	versions, err := st.Packages().Versions(ctx, id)
	if err != nil {
		return fmt.Errorf("npm: versions of package %d: %w", id, err)
	}
	_, ok, cs, err := s.holderOf(versions)
	if err != nil || ok {
		return err
	}
	return s.giveLatest(ctx, st, versions, cs, now)
}

// giveLatest tags latest the version of versions that outranks the others,
// cs being them as Candidates in the same order. [I] Whether 7.x, by
// default, then prefers the most recent deploy among pre-releases alone is
// unknown (#216).
func (s Settings) giveLatest(
	ctx context.Context,
	st store.Store,
	versions []types.PackageVersion,
	cs []Candidate,
	now time.Time,
) error {
	if len(versions) == 0 {
		return nil
	}
	top := s.Latest(cs)
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
