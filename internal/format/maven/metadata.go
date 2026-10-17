package maven

import (
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Deriver is recorded as the creator and modifier of the metadata the
// format writes: Artifactory's system user, SecurityService.USER_SYSTEM. It
// is how a run tells its own file from one a client uploaded, which it
// keeps when it has nothing to replace it with.
const Deriver = "_system_"

// modelVersion is the metadata model Artifactory writes
// (MavenArtifactInfo.MODEL_VERSION, with maven.metadata.publishModelVersion
// on by default).
const modelVersion = "1.1.0"

// maxMetadata bounds what a run reads of a stored metadata file or POM.
const maxMetadata = 1 << 20

// metadata is one maven-metadata.xml, in Maven's repository metadata model.
type metadata struct {
	XMLName    xml.Name    `xml:"metadata"`
	GroupID    string      `xml:"groupId"`
	ArtifactID string      `xml:"artifactId"`
	Version    string      `xml:"version"`
	Versioning *versioning `xml:"versioning"`
	Plugins    []plugin    `xml:"plugins>plugin"`
}

type versioning struct {
	Latest           string            `xml:"latest"`
	Release          string            `xml:"release"`
	Snapshot         *snapshot         `xml:"snapshot"`
	Versions         []string          `xml:"versions>version"`
	LastUpdated      string            `xml:"lastUpdated"`
	SnapshotVersions []snapshotVersion `xml:"snapshotVersions>snapshotVersion"`
}

type snapshot struct {
	Timestamp   string `xml:"timestamp"`
	BuildNumber int    `xml:"buildNumber"`
}

type snapshotVersion struct {
	Classifier string `xml:"classifier"`
	Extension  string `xml:"extension"`
	Value      string `xml:"value"`
	Updated    string `xml:"updated"`
}

type plugin struct {
	Name       string `xml:"name"`
	Prefix     string `xml:"prefix"`
	ArtifactID string `xml:"artifactId"`
}

// bytes writes m as Maven's MetadataXpp3Writer does, which is what
// Artifactory serves: its elements in the model's order, empty ones left
// out, a childless element closed in place, two-space indents.
func (m metadata) bytes() []byte {
	w := &xmlWriter{}
	w.b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	w.b.WriteString(`<metadata modelVersion="` + modelVersion + `">` + "\n")
	w.text(1, "groupId", m.GroupID)
	w.text(1, "artifactId", m.ArtifactID)
	w.text(1, "version", m.Version)
	if v := m.Versioning; v != nil {
		w.open(1, "versioning")
		w.text(2, "latest", v.Latest)
		w.text(2, "release", v.Release)
		if s := v.Snapshot; s != nil {
			if s.Timestamp == "" && s.BuildNumber == 0 {
				w.empty(2, "snapshot")
			} else {
				w.open(2, "snapshot")
				w.text(3, "timestamp", s.Timestamp)
				if s.BuildNumber != 0 {
					w.text(3, "buildNumber", strconv.Itoa(s.BuildNumber))
				}
				w.close(2, "snapshot")
			}
		}
		if len(v.Versions) > 0 {
			w.open(2, "versions")
			for _, ver := range v.Versions {
				w.text(3, "version", ver)
			}
			w.close(2, "versions")
		}
		w.text(2, "lastUpdated", v.LastUpdated)
		if len(v.SnapshotVersions) > 0 {
			w.open(2, "snapshotVersions")
			for _, sv := range v.SnapshotVersions {
				w.open(3, "snapshotVersion")
				w.text(4, "classifier", sv.Classifier)
				w.text(4, "extension", sv.Extension)
				w.text(4, "value", sv.Value)
				w.text(4, "updated", sv.Updated)
				w.close(3, "snapshotVersion")
			}
			w.close(2, "snapshotVersions")
		}
		w.close(1, "versioning")
	}
	if len(m.Plugins) > 0 {
		w.open(1, "plugins")
		for _, p := range m.Plugins {
			w.open(2, "plugin")
			w.text(3, "name", p.Name)
			w.text(3, "prefix", p.Prefix)
			w.text(3, "artifactId", p.ArtifactID)
			w.close(2, "plugin")
		}
		w.close(1, "plugins")
	}
	w.b.WriteString("</metadata>\n")
	return w.b.Bytes()
}

type xmlWriter struct{ b bytes.Buffer }

func (w *xmlWriter) indent(depth int) { w.b.WriteString(strings.Repeat("  ", depth)) }

func (w *xmlWriter) open(depth int, name string) {
	w.indent(depth)
	w.b.WriteString("<" + name + ">\n")
}

func (w *xmlWriter) close(depth int, name string) {
	w.indent(depth)
	w.b.WriteString("</" + name + ">\n")
}

func (w *xmlWriter) empty(depth int, name string) {
	w.indent(depth)
	w.b.WriteString("<" + name + "/>\n")
}

func (w *xmlWriter) text(depth int, name, value string) {
	if value == "" {
		return
	}
	w.indent(depth)
	w.b.WriteString("<" + name + ">")
	// Escaping into a bytes.Buffer cannot fail.
	_ = xml.EscapeText(&w.b, []byte(value))
	w.b.WriteString("</" + name + ">\n")
}

// timeStamp is lastUpdated's form: UTC, yyyyMMddHHmmss.
func timeStamp(t time.Time) string { return t.UTC().Format("20060102150405") }

// packageFile is a file the index links to one of a package's versions.
type packageFile struct {
	version string
	item    types.Item
}

// Versions returns the versions the index holds for package name in c,
// oldest first in Mercury order, as the artifact-level metadata lists them;
// none for a package the index does not know. Each comes with the files it
// links.
func Versions(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	name string,
) ([]types.PackageVersion, map[int64][]types.Item, error) {
	p, err := st.Packages().Find(ctx, c.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("maven: package %s: %w", name, err)
	}
	// The store orders versions by the package type's comparator, Mercury
	// for the Maven group (types.ComparatorFor).
	vs, err := st.Packages().Versions(ctx, p.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("maven: versions of %s: %w", name, err)
	}
	files := make(map[int64][]types.Item, len(vs))
	for _, v := range vs {
		ids, err := st.Packages().Files(ctx, v.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("maven: files of %s %s: %w", name, v.Version, err)
		}
		for _, id := range ids {
			it, err := st.Items().GetByID(ctx, id)
			if err != nil {
				return nil, nil, fmt.Errorf("maven: file %d of %s %s: %w", id, name, v.Version, err)
			}
			files[v.ID] = append(files[v.ID], it)
		}
	}
	return vs, files, nil
}

// writePackageMetadata rebuilds package name's maven-metadata.xml files in
// c from the package index: the artifact-level file in the package's
// folder, and each snapshot folder's. A file with nothing left to describe
// is removed (#279), except an artifact-level file a client uploaded.
func writePackageMetadata(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	r reader,
	name string,
) error {
	g, a, ok := strings.Cut(name, ":")
	if !ok {
		return nil
	}
	vs, byVersion, err := Versions(ctx, env.Store, c, name)
	if err != nil {
		return err
	}
	var files []packageFile
	for _, v := range vs {
		for _, it := range byVersion[v.ID] {
			files = append(files, packageFile{version: v.Version, item: it})
		}
	}
	now := env.Now()
	dir := packageDir(r.l, g, a)
	if dir != "" {
		if err := writeArtifactLevel(ctx, env, c, dir, g, a, files, now); err != nil {
			return err
		}
	}
	return writeSnapshotLevel(ctx, env, c, dir, g, a, files, now)
}

// packageDir is the folder c's layout keeps package g:a's version folders
// in, without its slash; empty when the layout has none, as Maven 1's,
// which files every version together.
func packageDir(l *layout.Layout, g, a string) string {
	p := l.Prefixes(layout.Values{layout.TokenOrg: g, layout.TokenModule: a})[0]
	if !strings.HasSuffix(p, "/"+a+"/") && p != a+"/" {
		return ""
	}
	return strings.TrimSuffix(p, "/")
}

// writeArtifactLevel writes the artifact-level metadata of the package in
// dir: its versions are the version folders in dir that hold a POM, as
// Artifactory's createVersionsMetadata lists them, in Mercury order.
func writeArtifactLevel(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	dir, g, a string,
	files []packageFile,
	now time.Time,
) error {
	withPom := map[string]bool{}
	for _, f := range files {
		p := f.item.FullPath()
		if IsPom(p) && path.Dir(path.Dir(p)) == dir {
			withPom[path.Base(path.Dir(p))] = true
		}
	}
	target := dir + "/" + MetadataFile
	if len(withPom) == 0 {
		return removeDerived(
			ctx,
			env,
			c,
			target,
			func(m metadata) bool { return len(m.Plugins) == 0 },
		)
	}
	// Mercury order, and versions it holds equal, such as 1.0 and 1.0.0, by
	// name, so every run lists them alike.
	order := slices.Sorted(maps.Keys(withPom))
	slices.SortStableFunc(order, types.CompareMercury)
	v := &versioning{Versions: order, LastUpdated: timeStamp(now)}
	v.Latest = order[len(order)-1]
	for _, ver := range order {
		if !IsSnapshotVersion(ver) {
			v.Release = ver
		}
	}
	m := metadata{GroupID: g, ArtifactID: a, Version: v.Latest, Versioning: v}
	return derive(ctx, env, c, target, m)
}

// writeSnapshotLevel writes the metadata of each of the package's snapshot
// folders that holds a POM, and removes it from those in dir that no longer
// do.
func writeSnapshotLevel(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	dir, g, a string,
	files []packageFile,
	now time.Time,
) error {
	byFolder := map[string][]types.Item{}
	withPom := map[string]bool{}
	for _, f := range files {
		folder := path.Dir(f.item.FullPath())
		if !IsSnapshotVersion(path.Base(folder)) {
			continue
		}
		byFolder[folder] = append(byFolder[folder], f.item)
		if IsPom(f.item.Name) {
			withPom[folder] = true
		}
	}
	for _, folder := range slices.Sorted(maps.Keys(withPom)) {
		m := snapshotMetadata(c, g, a, folder, byFolder[folder], now)
		if err := derive(ctx, env, c, folder+"/"+MetadataFile, m); err != nil {
			return err
		}
	}
	if dir == "" {
		return nil
	}
	stale, err := snapshotMetadataIn(ctx, env.Store, c, dir)
	if err != nil {
		return err
	}
	for _, p := range stale {
		if withPom[path.Dir(p)] {
			continue
		}
		// A snapshot folder's metadata is always the server's under unique
		// and non-unique, which drop the client's (#278), so it goes
		// whoever wrote it, as Artifactory's removeMetadataIfExist removes
		// it.
		if err := env.Underive(ctx, c, false, p); err != nil {
			return fmt.Errorf("maven: remove %s: %w", p, err)
		}
	}
	return nil
}

// snapshotMetadataIn lists the maven-metadata.xml files of the snapshot
// folders directly in dir.
func snapshotMetadataIn(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	dir string,
) ([]string, error) {
	q := &aql.Query{Criteria: append(
		files(c),
		&aql.FieldCriterion{
			Field: aql.FieldPath,
			Op:    aql.OpMatch,
			Text:  dir + "/*" + SnapshotSuffix,
		},
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: MetadataFile},
	)}
	hits, err := st.Items().Search(ctx, q, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("maven: snapshot metadata under %s: %w", dir, err)
	}
	var out []string
	for _, h := range hits {
		p := h.Item.FullPath()
		if !h.Item.Cached && path.Dir(path.Dir(p)) == dir {
			out = append(out, p)
		}
	}
	return out, nil
}

// m2 reads unique snapshot names as Artifactory's snapshot metadata does,
// with maven-2-default whatever the repository's layout.
var m2 = func() *layout.Layout {
	l, err := layout.Lookup(repo.LayoutMaven2)
	if err != nil {
		panic("maven: " + err.Error())
	}
	return l
}()

// build is one unique snapshot file.
type build struct {
	UniqueBuild
	co layout.Coordinates
}

// newer orders builds by build number, then timestamp, as
// BuildNumberSnapshotComparator does.
func newer(a, b UniqueBuild) bool {
	if a.Number != b.Number {
		return a.Number > b.Number
	}
	return a.Timestamp > b.Timestamp
}

// snapshotMetadata is the metadata of a snapshot folder with a POM, from
// its files, as Artifactory's createSnapshotsMetadata writes it under
// unique and non-unique, except for <version>, which is the folder's base
// version where Artifactory cuts it at the first hyphen (DV-050).
func snapshotMetadata(
	c repo.Config,
	g, a, folder string,
	its []types.Item,
	now time.Time,
) metadata {
	v := &versioning{LastUpdated: timeStamp(now), Snapshot: &snapshot{}}
	m := metadata{GroupID: g, ArtifactID: a, Version: path.Base(folder), Versioning: v}
	if c.SnapshotVersionBehavior == repo.NonUnique {
		v.Snapshot.BuildNumber = 1
		return m
	}
	var latestPom *UniqueBuild
	latest := map[[2]string]build{}
	for _, it := range its {
		b, ok := ParseUniqueBuild(it.Name)
		if !ok {
			continue
		}
		if IsPom(it.Name) && (latestPom == nil || newer(b, *latestPom)) {
			latestPom = &b
		}
		co, ok := m2.Read(it.FullPath())
		if !ok || co.FileItegRev == "" {
			continue
		}
		k := [2]string{co.Ext, co.Classifier}
		if cur, seen := latest[k]; !seen || newer(b, cur.UniqueBuild) {
			latest[k] = build{b, co}
		}
	}
	if latestPom != nil {
		v.Snapshot.Timestamp, v.Snapshot.BuildNumber = latestPom.Timestamp, latestPom.Number
	}
	for _, k := range slices.SortedFunc(maps.Keys(latest), func(x, y [2]string) int {
		return cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1]))
	}) {
		b := latest[k]
		v.SnapshotVersions = append(v.SnapshotVersions, snapshotVersion{
			Classifier: b.co.Classifier,
			Extension:  b.co.Ext,
			Value:      b.co.BaseRev + "-" + b.co.FileItegRev,
			Updated:    strings.ReplaceAll(b.Timestamp, ".", ""),
		})
	}
	return m
}

// derive writes m at p as the server's metadata.
func derive(ctx context.Context, env format.Env, c repo.Config, p string, m metadata) error {
	if _, err := env.Derive(ctx, format.Derived{
		Repo: c, Path: p, Body: bytes.NewReader(m.bytes()), By: Deriver,
	}); err != nil {
		return fmt.Errorf("maven: write %s: %w", p, err)
	}
	return nil
}

// removeDerived removes the metadata at p when the format wrote it and
// mine accepts what it says: a folder's file may hold another level's
// metadata as well, which the other run keeps.
func removeDerived(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	p string,
	mine func(metadata) bool,
) error {
	m, it, err := stored(ctx, env, c, p)
	if err != nil || it.ID == 0 || it.ModifiedBy != Deriver || !mine(m) {
		return err
	}
	if err := env.Underive(ctx, c, false, p); err != nil {
		return fmt.Errorf("maven: remove %s: %w", p, err)
	}
	return nil
}

// stored reads the metadata file at p, if there is one. A file that does
// not parse reads as empty metadata.
func stored(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	p string,
) (metadata, types.Item, error) {
	it, err := env.Store.Items().Get(ctx, c.Key, false, p)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return metadata{}, types.Item{}, nil
	case err != nil:
		return metadata{}, types.Item{}, fmt.Errorf("maven: get %s: %w", p, err)
	case it.Folder:
		return metadata{}, types.Item{}, nil
	}
	body, err := content(ctx, env, it)
	if err != nil {
		return metadata{}, types.Item{}, err
	}
	var m metadata
	_ = xml.Unmarshal(body, &m)
	return m, it, nil
}

// content reads up to maxMetadata bytes of a stored file.
func content(ctx context.Context, env format.Env, it types.Item) ([]byte, error) {
	rc, _, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("maven: open %s: %w", it.FullPath(), err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxMetadata))
	if err != nil {
		return nil, fmt.Errorf("maven: read %s: %w", it.FullPath(), err)
	}
	return body, nil
}
