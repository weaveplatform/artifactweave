package maven

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// buildTimestamp is the matrix parameter that dates a deploy's build: the
// JFrog CLI and its Maven extractor send it, in milliseconds since the
// epoch, so one build's files share one unique timestamp (research 27
// section 2.4).
const buildTimestamp = "build.timestamp"

// uniqueTimestamp is a unique build's timestamp form, yyyyMMdd.HHmmss in
// UTC (MavenModelUtils.UNIQUE_SNAPSHOT_FORMATTER).
const uniqueTimestamp = "20060102.150405"

// snapshotWrite returns a deploy, or a checksum upload of the file w.Path
// names, as it lands under c's snapshotVersionBehavior (6.20's
// SnapshotVersionAdapterBase and its three adapters, research 27 section 2,
// decision 0036). Only a local repository with the Maven layout renames, and
// only a snapshot file the layout reads: metadata and other files land as
// sent, and so does every file under deployer.
func snapshotWrite(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	r reader,
	w format.Write,
) (format.Write, error) {
	if c.Class != repo.Local || w.Cached || !IsMavenLayout(r.l) || IsMetadata(w.Path) {
		return w, nil
	}
	co, ok := Read(r.l, w.Path)
	if !ok || !co.Integration() {
		return w, nil
	}
	var err error
	switch c.SnapshotVersionBehavior {
	case repo.Deployer:
	case repo.NonUnique:
		w.Path = nonUniquePath(w.Path, co)
	default:
		w.Path, err = uniquePath(ctx, env, c, r, w, co)
	}
	return w, err
}

// nonUniquePath names a unique build of the folder's base version with
// -SNAPSHOT, so every build overwrites the last
// (NonUniqueSnapshotVersionAdapter).
func nonUniquePath(p string, co Coordinates) string {
	name := path.Base(p)
	b, ok := ParseUniqueBuild(name)
	if !ok || !strings.Contains(name, co.BaseRev+"-"+b.String()) {
		return p
	}
	return path.Join(path.Dir(p), strings.ReplaceAll(name, b.String(), Snapshot))
}

// uniquePath names a -SNAPSHOT file of the folder's base version with a
// build (UniqueSnapshotVersionAdapter): the build a build.timestamp names,
// for a checksum the newest build of its file, and otherwise the build
// Artifactory's rules for its kind of file choose. A name that is already
// unique, or names another version, is kept.
func uniquePath(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	r reader,
	w format.Write,
	co Coordinates,
) (string, error) {
	name := path.Base(w.Path)
	if IsUniqueSnapshotName(name) || !strings.Contains(name, co.BaseRev+SnapshotSuffix) {
		return w.Path, nil
	}
	builds, err := folderBuilds(ctx, env, c, path.Dir(w.Path))
	if err != nil {
		return "", err
	}
	var b UniqueBuild
	switch ms := clientTimestamp(w.Properties); {
	case ms > 0:
		b = builds.byTimestamp(time.UnixMilli(ms).UTC().Format(uniqueTimestamp))
	case w.Op == format.OpChecksum:
		var ok bool
		if b, ok = builds.newestOf(w.Path); !ok {
			// No build of the file: the path as sent, which the upload
			// then finds missing.
			return w.Path, nil
		}
	default:
		b = builds.next(co, IsPom(w.Path), env.Now())
	}
	return buildPath(r.l, w.Path, co, b)
}

// clientTimestamp reads a deploy's build.timestamp; a value that is not a
// number is ignored, as MavenSnapshotVersionAdapterContext.setTimestamp
// logs and ignores it.
func clientTimestamp(props types.Properties) int64 {
	vs := props[buildTimestamp]
	if len(vs) == 0 {
		return 0
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(vs[0]), 10, 64)
	if err != nil {
		return 0
	}
	return ms
}

// buildPath is p with b as its file's integration revision, rendered by
// the Maven layout as ModuleInfoUtils.constructArtifactPath does.
func buildPath(l *layout.Layout, p string, co Coordinates, b UniqueBuild) (string, error) {
	v, err := l.Match(p)
	if err != nil {
		return "", fmt.Errorf("maven: read %s: %w", p, err)
	}
	v[layout.TokenFileItegRev] = b.String()
	if v[layout.TokenExt] == "" {
		v[layout.TokenExt] = co.Ext
	}
	out, err := l.Build(v)
	if err != nil {
		return "", fmt.Errorf("maven: name a build of %s: %w", p, err)
	}
	return out, nil
}

// snapshotFile is a unique build's file in a snapshot folder.
type snapshotFile struct {
	path  string
	build UniqueBuild
}

// builds are the unique files of one snapshot folder, in name order.
type builds []snapshotFile

// folderBuilds lists the unique files directly in folder, read in the
// write's own transaction, so a build numbered by one deploy is seen by
// the next.
func folderBuilds(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	folder string,
) (builds, error) {
	q := &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: c.Key}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpEq, Text: folder},
	}}
	hits, err := env.Store.Items().Search(ctx, q, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("maven: files of %s: %w", folder, err)
	}
	var out builds
	for _, h := range hits {
		if b, ok := ParseUniqueBuild(h.Item.Name); ok {
			out = append(out, snapshotFile{path: h.Item.FullPath(), build: b})
		}
	}
	slices.SortFunc(out, func(a, b snapshotFile) int { return strings.Compare(a.path, b.path) })
	return out, nil
}

// current is the highest build number in the folder, 0 for none.
func (bs builds) current() int {
	n := 0
	for _, f := range bs {
		n = max(n, f.build.Number)
	}
	return n
}

// find returns the first file that matches.
func (bs builds) find(match func(snapshotFile) bool) (snapshotFile, bool) {
	for _, f := range bs {
		if match(f) {
			return f, true
		}
	}
	return snapshotFile{}, false
}

// stamp is the timestamp of build n, or now for a build no file has yet.
func (bs builds) stamp(n int, now time.Time) string {
	if f, ok := bs.find(func(f snapshotFile) bool { return f.build.Number == n }); ok {
		return f.build.Timestamp
	}
	return now.UTC().Format(uniqueTimestamp)
}

// byTimestamp is the build a client's timestamp names: the one a file
// already carries, or else the next number (adjustUsingClientTimestamp).
func (bs builds) byTimestamp(ts string) UniqueBuild {
	if f, ok := bs.find(func(f snapshotFile) bool { return f.build.Timestamp == ts }); ok {
		return UniqueBuild{Timestamp: ts, Number: f.build.Number}
	}
	return UniqueBuild{Timestamp: ts, Number: bs.current() + 1}
}

// next is the build a file named with -SNAPSHOT joins without a client
// timestamp (adjustUsingServerData). A main artifact starts the next build.
// A classifier artifact joins the current one, as it follows its main
// artifact. A POM joins the current build unless that build already has a
// POM, since the main artifact may have come first.
func (bs builds) next(co Coordinates, pom bool, now time.Time) UniqueBuild {
	cur := bs.current()
	n := cur + 1
	switch {
	case cur == 0:
	case co.Classifier != "":
		n = cur
	case pom:
		if _, ok := bs.find(func(f snapshotFile) bool {
			return f.build.Number == cur && IsPom(f.path)
		}); !ok {
			n = cur
		}
	}
	return UniqueBuild{Timestamp: bs.stamp(n, now), Number: n}
}

// newestOf is the highest build of the file a -SNAPSHOT checksum names:
// the unique file whose name, with -SNAPSHOT for its build, is p
// (adjustChecksum). Of two with one number the first by name wins.
func (bs builds) newestOf(p string) (UniqueBuild, bool) {
	var out UniqueBuild
	found := false
	for _, f := range bs {
		name := strings.ReplaceAll(path.Base(f.path), f.build.String(), Snapshot)
		if path.Join(path.Dir(f.path), name) == p && (!found || f.build.Number > out.Number) {
			out, found = f.build, true
		}
	}
	return out, found
}
