package maven

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The version tokens a download path may carry.
const (
	tokenRelease     = "[RELEASE]"
	tokenIntegration = "[INTEGRATION]"
)

// Resolve rewrites a GET or HEAD that asks for the latest of something
// (JFrog's Retrieve Artifact, research 27 section 8, decision 0036), in a
// local repository or a remote repository's cache, the repositories JFrog
// documents; a virtual repository resolves in each member it searches.
//   - Under the Maven layout, a non-unique snapshot name names its folder's
//     newest unique build, unless the repository stores non-unique names,
//     and [RELEASE] the highest release in the Mercury order.
//   - Under any other layout, [RELEASE] and [INTEGRATION] name the
//     alphabetically last release, or integration revision of the path's
//     base revision, as JFrog documents.
//
// Either way a candidate has the path's coordinates and matches the
// request's matrix parameters. Candidates are the files themselves, never
// maven-metadata.xml, so a resolution does not wait on the metadata the work
// queue writes (#279). A path that resolves to nothing is looked up as it is.
func (Handler) Resolve(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	d format.Download,
) (string, error) {
	if c.Class == repo.Virtual || (c.Class == repo.Remote) != d.Cached {
		return d.Path, nil
	}
	r, err := readerOf(ctx, env, c)
	if err != nil || r.l == nil {
		return d.Path, err
	}
	res := resolution{st: env.Store, c: c, r: r, d: d, maven: IsMavenLayout(r.l)}
	switch {
	case res.maven && IsNonUniqueSnapshot(d.Path):
		return res.snapshot(ctx)
	case strings.Contains(d.Path, tokenRelease):
		return res.tokens(ctx, true)
	// Artifactory reads [INTEGRATION] under the Maven layout as nothing at
	// all: -SNAPSHOT asks for its integration revisions there.
	case !res.maven && strings.Contains(d.Path, tokenIntegration):
		return res.tokens(ctx, false)
	}
	return d.Path, nil
}

// resolution is one download being resolved.
type resolution struct {
	st    store.Store
	c     repo.Config
	r     reader
	d     format.Download
	maven bool
}

// candidate is a file a resolution may name, as the repository's layout
// reads it.
type candidate struct {
	types.Item
	co    layout.Coordinates
	props types.Properties
}

// matches reports a candidate with want's coordinates that matches the
// request's matrix parameters.
func (res resolution) matches(want layout.Coordinates, f candidate) bool {
	return sameModule(want, f.co) && res.d.Matrix.Matches(f.props)
}

// snapshot resolves a non-unique snapshot name to the newest file of its
// folder with the same coordinates (LocalLatestVersionResolver
// .getLatestArtifactPath in 6.20): the latest by Mercury order of base and
// file revision, or under deployer the last modified. Artifactory lists the
// folder as the caller browses it, so files the caller may not read, and
// files the repository's patterns exclude, take no part.
func (res resolution) snapshot(ctx context.Context) (string, error) {
	if res.c.SnapshotVersionBehavior == repo.NonUnique {
		return res.d.Path, nil
	}
	want, ok := Read(res.r.l, res.d.Path)
	if !ok {
		return res.d.Path, nil
	}
	dir := path.Dir(res.d.Path)
	files, err := res.search(
		ctx,
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpEq, Text: dir},
	)
	if err != nil {
		return "", err
	}
	// The listing is in name order; ties keep the first by name under
	// deployer and the last otherwise, as Artifactory's comparisons do.
	slices.SortFunc(files, func(a, b candidate) int { return strings.Compare(a.Name, b.Name) })
	patterns := authz.PatternsOf(res.c)
	var latest *candidate
	for i, f := range files {
		switch {
		case res.d.Readable != nil && !res.d.Readable(f.FullPath()),
			!patterns.Accepts(f.FullPath(), false),
			!res.matches(want.Coordinates, f):
			continue
		case latest == nil,
			res.c.SnapshotVersionBehavior == repo.Deployer && f.Modified.After(latest.Modified),
			res.c.SnapshotVersionBehavior != repo.Deployer && compareLatest(f.co, latest.co) >= 0:
			latest = &files[i]
		}
	}
	if latest == nil {
		return res.d.Path, nil
	}
	return latest.FullPath(), nil
}

// tokens resolves [RELEASE] (release) or [INTEGRATION]
// (LocalLatestVersionResolver.getLatestVersionRequestContext in 6.20):
// among the module's release files, or its integration files of the path's
// base revision, those with the path's coordinates, latest first, name the
// first whose path holds the request with its token replaced by the file's
// revision, or else the first.
func (res resolution) tokens(ctx context.Context, release bool) (string, error) {
	tl, err := res.r.l.VersionTokens()
	if err != nil {
		return "", fmt.Errorf("maven: version tokens of %s: %w", res.c.Key, err)
	}
	want, ok := Read(tl, res.d.Path)
	if !ok {
		return res.d.Path, nil
	}
	values := layout.Values{layout.TokenOrg: want.Org, layout.TokenModule: want.Module}
	if !release {
		values[layout.TokenBaseRev] = want.BaseRev
	}
	files, err := res.search(ctx, under(res.r.l.Prefixes(values)))
	if err != nil {
		return "", err
	}
	latest, token := res.releases(want.Coordinates, files), tokenRelease
	if !release {
		latest, token = res.integrations(want.Coordinates, files), tokenIntegration
	}
	for _, f := range latest {
		rev := f.co.BaseRev
		if !release {
			rev = f.co.FileItegRev
		}
		// A file without a file revision has nothing to put in the token's
		// place; Artifactory's String.replace would fail on it, so it can
		// only be the fallback.
		if rev != "" && strings.Contains(f.FullPath(), strings.ReplaceAll(res.d.Path, token, rev)) {
			return f.FullPath(), nil
		}
	}
	if len(latest) == 0 {
		return res.d.Path, nil
	}
	return latest[0].FullPath(), nil
}

// releases orders the module's release files with want's coordinates
// highest first: by the Mercury order of their base revision under the Maven
// layout (getRequestContentForReleaseByVersion in 6.20), and alphabetically
// under any other. Ties keep the order the files were collected in, newest
// first by creation, then by name descending, as Artifactory's
// ReleaseVersionsRetriever collects them.
func (res resolution) releases(want layout.Coordinates, files []candidate) []candidate {
	var out []candidate
	for _, f := range files {
		if !f.co.Integration() && res.matches(want, f) {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, newestFirst)
	if res.maven {
		slices.SortStableFunc(out, func(a, b candidate) int { return compareLatest(b.co, a.co) })
	} else {
		slices.SortStableFunc(
			out,
			func(a, b candidate) int { return strings.Compare(b.co.BaseRev, a.co.BaseRev) },
		)
	}
	return out
}

// integrations orders the integration files of want's base revision with
// want's coordinates by their file integration revision, alphabetically
// last first. Where the layout's two integration revision expressions
// differ, a file whose folder and file revisions are the same is a
// non-unique one and takes no part (SnapshotVersionsRetriever in 6.20).
func (res resolution) integrations(want layout.Coordinates, files []candidate) []candidate {
	def := res.r.l.Definition()
	same := def.FolderIntegrationRevisionRegExp == def.FileIntegrationRevisionRegExp
	var out []candidate
	for _, f := range files {
		if f.co.Integration() && f.co.BaseRev == want.BaseRev &&
			(same || f.co.FolderItegRev != f.co.FileItegRev) && res.matches(want, f) {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, newestFirst)
	slices.SortStableFunc(
		out,
		func(a, b candidate) int { return strings.Compare(b.co.FileItegRev, a.co.FileItegRev) },
	)
	return out
}

// newestFirst orders by creation descending, then by name descending, as
// Artifactory's reversed TreeMultimap of creation dates to items does.
func newestFirst(a, b candidate) int {
	return cmp.Or(b.Created.Compare(a.Created), strings.Compare(b.Name, a.Name))
}

// search returns the files of c, or of its cache, that match criterion and
// that its layout reads, with their coordinates, and their properties when
// the request has matrix parameters to match.
func (res resolution) search(ctx context.Context, criterion aql.Node) ([]candidate, error) {
	q := &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: res.c.Key, Cached: res.d.Cached}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		criterion,
	}}
	if len(res.d.Matrix.Props) > 0 {
		q.Include = []string{"property.*"}
	}
	hits, err := res.st.Items().Search(ctx, q, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("maven: resolve %s in %s: %w", res.d.Path, res.c.Key, err)
	}
	var out []candidate
	for _, h := range hits {
		if co, ok := res.r.read(h.Item.FullPath()); ok {
			out = append(out, candidate{Item: h.Item, co: co.Coordinates, props: h.Properties})
		}
	}
	return out, nil
}

// sameModule is LocalLatestVersionResolver.areModuleInfosTheSame: the same
// organisation, module, classifier and extension, and for an integration
// request the same base revision.
func sameModule(want, got layout.Coordinates) bool {
	return want.Org == got.Org && want.Module == got.Module &&
		want.Classifier == got.Classifier && want.Ext == got.Ext &&
		(!want.Integration() || want.BaseRev == got.BaseRev)
}

// compareLatest is LocalLatestVersionResolver.compareLatest: the Mercury
// order of the base revision with the file's integration revision.
// Artifactory joins a missing revision as the text "null" unless both lack
// one, which is kept so a file without one sorts as it does there.
func compareLatest(a, b layout.Coordinates) int {
	if a.FileItegRev == "" && b.FileItegRev == "" {
		return types.CompareMercury(a.BaseRev, b.BaseRev)
	}
	return types.CompareMercury(
		a.BaseRev+"-"+orNull(a.FileItegRev),
		b.BaseRev+"-"+orNull(b.FileItegRev),
	)
}

func orNull(rev string) string { return cmp.Or(rev, "null") }
