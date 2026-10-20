package pypi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// entry is how a file is indexed: under a project's version, as a record
// with its core metadata text.
type entry struct {
	project, version string
	file             File
	text             string
}

// entryOf is how the file it, with core metadata d (nil for none) and
// properties props, is indexed; ok is false for a file the index leaves
// out (decision 0037). The hooks and a reindex both read a file through
// it, so they agree.
//   - Core metadata with a valid Name and Version decides the project and
//     version, whatever the properties say: content wins.
//   - Without it, pypi.name and pypi.version do, as JFrog requires of a
//     manual upload it cannot read, and pypi.requires.python gives the
//     file's Requires-Python, as 7.x's project page query reads it
//     (RTFACT-29892). [I] That 7.x reads it for such a file is inferred
//     (#216).
func entryOf(it types.Item, d *Dist, props types.Properties) (entry, bool) {
	e := entry{file: File{
		Path: it.FullPath(), Name: path.Base(it.FullPath()),
		SHA256: it.Actual.SHA256, MD5: it.Actual.MD5, Size: it.Size, Uploaded: uploaded(it),
	}}
	if it.Folder || !IsDistribution(e.file.Path) {
		return e, false
	}
	e.file.Yanked, e.file.YankedReason = yankedBy(props)
	if d != nil {
		e.project, e.version = Normalize(d.Name), NormalizeVersion(d.Version)
		e.file.RequiresPython, e.text = d.RequiresPython, d.Metadata
		e.file.Metadata, e.file.Core = textKey(d.Metadata), d.Core
		return e, true
	}
	name, version := first(props, PropName), strings.TrimSpace(first(props, PropVersion))
	if !ValidName(name) || !validVersion(version) {
		return e, false
	}
	e.project, e.version = Normalize(name), NormalizeVersion(version)
	e.file.RequiresPython = first(props, PropRequiresPython)
	return e, true
}

// first is the first value of the property key, or "".
func first(props types.Properties, key string) string {
	if v := props[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// yankedBy reads a file's yanked state and reason from pypi.yanked
// (PropYanked).
func yankedBy(props types.Properties) (bool, string) {
	v, ok := props[PropYanked]
	if !ok {
		return false, ""
	}
	var s string
	if len(v) > 0 {
		s = v[0]
	}
	switch {
	case strings.EqualFold(s, "false"):
		return false, ""
	case s == "" || strings.EqualFold(s, "true"):
		return true, ""
	}
	return true, s
}

// setProperties sets the pypi.* properties d gives on it, removing a
// summary or Requires-Python d no longer has, and returns its properties
// as they then are. A deploy's own pypi.* values that disagree with the
// content are replaced: content wins (decision 0037). Writing them
// straight to the store runs no Annotated hook, and they are written at
// the time the content was, it.Updated, so they read as part of the
// deploy.
func setProperties(
	ctx context.Context,
	st store.Store,
	it types.Item,
	d *Dist,
) (types.Properties, error) {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the item
	}
	want := types.Properties{
		PropName:           {d.Name},
		PropVersion:        {d.Version},
		PropNormalizedName: {Normalize(d.Name)},
	}
	optional := map[string]string{PropSummary: d.Summary, PropRequiresPython: d.RequiresPython}
	var remove []string
	for _, k := range slices.Sorted(maps.Keys(optional)) {
		switch v := propertyValue(optional[k]); {
		case v != "":
			want[k] = []string{v}
		case props[k] != nil:
			remove = append(remove, k)
		}
	}
	set := types.Properties{}
	for k, v := range want {
		if !slices.Equal(props[k], v) {
			set[k] = v
		}
	}
	if len(set) == 0 && len(remove) == 0 {
		return props, nil
	}
	if err := st.Items().PatchProperties(ctx, it.ID, set, remove, it.Updated); err != nil {
		return nil, err //nolint:wrapcheck // the caller names the item
	}
	// A store may give an item without properties none at all.
	out := types.Properties{}
	maps.Copy(out, props)
	for _, k := range remove {
		delete(out, k)
	}
	maps.Copy(out, set)
	return out, nil
}

// propertyValue is s as a property can hold it: without NUL, which
// PostgreSQL refuses in text, and cut as the store would cut it, so that
// it compares with what the store holds. The name and version are valid
// by now, and need neither.
func propertyValue(s string) string {
	return types.TruncatePropertyValue(strings.ReplaceAll(s, "\x00", ""), types.PropertyValueMax)
}

// place indexes the file it as e says, or leaves it out of the index when
// ok is false, taking it from any other version it was a file of.
func place(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	it types.Item,
	e entry,
	ok bool,
) error {
	old, linked, err := pkgindex.VersionOf(ctx, st, it.ID)
	if err != nil {
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	if linked {
		p, err := st.Packages().Get(ctx, old.PackageID)
		if err != nil {
			return fmt.Errorf("pypi: project %d: %w", old.PackageID, err)
		}
		same := ok && p.Name == e.project
		if !same || old.Version != e.version {
			if err := detach(ctx, st, p, old, it, same); err != nil {
				return err
			}
		}
	}
	if !ok {
		return nil
	}
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: e.project, Type: c.PackageType, Created: e.file.Uploaded,
	})
	if err != nil {
		return fmt.Errorf("pypi: project %s: %w", e.project, err)
	}
	return attach(ctx, st, p, e, it.ID)
}

// attach makes e's record, of the item with id, a file of e's version of
// p.
func attach(ctx context.Context, st store.Store, p types.Package, e entry, id int64) error {
	var m Metadata
	var files []int64
	pv, err := st.Packages().Version(ctx, p.ID, e.version)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return fmt.Errorf("pypi: version %s of %s: %w", e.version, p.Name, err)
	default:
		if m, err = DecodeMetadata(pv.Metadata); err != nil {
			return err
		}
		if files, err = st.Packages().Files(ctx, pv.ID); err != nil {
			return fmt.Errorf("pypi: files of %s %s: %w", p.Name, e.version, err)
		}
	}
	m.put(e.file, e.text)
	if !slices.Contains(files, id) {
		files = append(files, id)
	}
	_, err = pkgindex.Sync(ctx, st, p, pkgindex.IndexedVersion{
		Version: e.version, Created: m.created(), Metadata: m.encode(), Files: files,
	})
	return err //nolint:wrapcheck // pkgindex wraps its own
}

// detach takes the file it out of pv, a version of p, dropping pv when it
// was its last file, and p when that was its last version, unless keep
// says the file stays in p. The store cannot unlink one file, so a version
// that keeps others is relinked to them (pkgindex.Sync).
func detach(
	ctx context.Context,
	st store.Store,
	p types.Package,
	pv types.PackageVersion,
	it types.Item,
	keep bool,
) error {
	m, err := DecodeMetadata(pv.Metadata)
	if err != nil {
		return err
	}
	m.drop(it.FullPath())
	files, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return fmt.Errorf("pypi: files of %s %s: %w", p.Name, pv.Version, err)
	}
	files = slices.DeleteFunc(files, func(id int64) bool { return id == it.ID })
	if len(files) > 0 {
		_, err = pkgindex.Sync(ctx, st, p, pkgindex.IndexedVersion{
			Version: pv.Version, Created: m.created(), Metadata: m.encode(), Files: files,
		})
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
		return fmt.Errorf("pypi: drop %s %s: %w", p.Name, pv.Version, err)
	}
	if keep {
		return nil
	}
	rest, err := st.Packages().Versions(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("pypi: versions of %s: %w", p.Name, err)
	}
	if len(rest) > 0 {
		return nil
	}
	if err := st.Packages().Delete(ctx, p.ID); err != nil {
		return fmt.Errorf("pypi: drop %s: %w", p.Name, err)
	}
	return nil
}

// fileQuery finds the files c's index reads, a remote repository's in its
// cache (pkgindex.Stored), with their properties when props is set. Which
// are distributions is decided by IsDistribution, as the hooks decide it.
func fileQuery(c repo.Config, props bool) *aql.Query {
	q := &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}
	if props {
		q.Include = []string{"property"}
	}
	return q
}

// reindex brings the index of c in line with its files and their
// properties. The distributions are read first, outside any transaction,
// so no decompression holds the store's write lock; the transaction then
// reads the tree again and reads only content that changed in between.
func reindex(ctx context.Context, env format.Env, c repo.Config) error {
	st := env.Store
	hits, err := st.Items().Search(ctx, fileQuery(c, false), aql.Options{})
	if err != nil {
		return fmt.Errorf("pypi: files of %s: %w", c.Key, err)
	}
	read := map[string]*Dist{}
	for _, h := range hits {
		if _, err := readOnce(ctx, env, read, h.Item); err != nil {
			return err
		}
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		hits, err := st.Items().Search(ctx, fileQuery(c, true), aql.Options{})
		if err != nil {
			return fmt.Errorf("pypi: files of %s: %w", c.Key, err)
		}
		type version struct{ project, version string }
		metas := map[version]*Metadata{}
		files := map[version][]int64{}
		for _, h := range hits {
			d, err := readOnce(ctx, env, read, h.Item)
			if err != nil {
				return err
			}
			e, ok := entryOf(h.Item, d, h.Properties)
			if !ok {
				continue
			}
			v := version{e.project, e.version}
			if metas[v] == nil {
				metas[v] = &Metadata{}
			}
			metas[v].put(e.file, e.text)
			files[v] = append(files[v], h.Item.ID)
		}
		projects := map[string]*pkgindex.Indexed{}
		for _, v := range slices.SortedFunc(maps.Keys(metas), func(a, b version) int {
			return cmp.Or(cmp.Compare(a.project, b.project), cmp.Compare(a.version, b.version))
		}) {
			m := metas[v]
			p := projects[v.project]
			if p == nil {
				p = &pkgindex.Indexed{Name: v.project, Created: m.created()}
				projects[v.project] = p
			}
			if m.created().Before(p.Created) {
				p.Created = m.created()
			}
			p.Versions = append(p.Versions, pkgindex.IndexedVersion{
				Version: v.version, Created: m.created(), Metadata: m.encode(), Files: files[v],
			})
		}
		want := make([]pkgindex.Indexed, 0, len(projects))
		for _, p := range projects {
			want = append(want, *p)
		}
		return pkgindex.Reconcile(ctx, st, c, want) //nolint:wrapcheck // pkgindex wraps its own
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// readOnce reads the distribution it, once per content and file name: nil
// when the format cannot index its content or it is of no distribution
// type.
func readOnce(
	ctx context.Context,
	env format.Env,
	read map[string]*Dist,
	it types.Item,
) (*Dist, error) {
	name := path.Base(it.FullPath())
	if !IsDistribution(name) {
		return nil, nil //nolint:nilnil // no distribution to read is no failure
	}
	key := it.Actual.SHA256 + "/" + name
	if d, ok := read[key]; ok {
		return d, nil
	}
	d, _, err := ReadStored(ctx, env.Blobs, it.Actual.SHA256, name)
	if err != nil {
		return nil, err
	}
	read[key] = d
	return d, nil
}
