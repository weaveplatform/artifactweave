package docker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// pageSize is how many rows each read of a folder or a repository's
// packages asks for.
const pageSize = 200

// imageFolder splits the parent path of a file into its image and the
// folder directly below the image that holds it. A file at the repository
// root or in a top-level folder belongs to no image.
func imageFolder(it types.Item) (name, folder string, ok bool) {
	if it.Folder {
		return "", "", false
	}
	i := strings.LastIndexByte(it.Path, '/')
	if i <= 0 || i == len(it.Path)-1 {
		return "", "", false
	}
	return it.Path[:i], it.Path[i+1:], true
}

func isManifest(file string) bool { return file == ManifestFile || file == ListManifestFile }

// PushTime is when a stored manifest was last written, its tag's place in
// the order of decision 0018: a later push of a different manifest
// replaces the file and so moves the tag to the newest place. A move keeps
// the time; a copy is a push at its target.
func PushTime(m types.Item) time.Time {
	if m.Modified.IsZero() {
		return m.Created
	}
	return m.Modified
}

// manifestQuery finds the manifest files of the image name in repoKey,
// each in a folder directly below the image (decision 0031), narrowed by
// more criteria.
func manifestQuery(repoKey, name string, more ...aql.Node) *aql.Query {
	criteria := make(aql.And, 0, 5+len(more))
	criteria = append(criteria,
		aql.RepoSet{Refs: []aql.RepoRef{{Key: repoKey}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		aql.Or{
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ManifestFile},
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ListManifestFile},
		},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: name + "/*"},
		&aql.FieldCriterion{
			Field: aql.FieldDepth, Op: aql.OpEq, Number: int64(strings.Count(name, "/") + 3),
		},
	)
	return &aql.Query{Criteria: append(criteria, more...)}
}

// firstManifest returns the image's earliest stored manifest; ok is false
// when it holds none.
func firstManifest(
	ctx context.Context,
	items store.ItemStore,
	repoKey, name string,
) (types.Item, bool, error) {
	q := manifestQuery(repoKey, name)
	q.Sort = &aql.Sort{Fields: []aql.Field{aql.FieldCreated}}
	q.Limit, q.HasLimit = 1, true
	hits, err := items.Search(ctx, q, aql.Options{})
	if err != nil {
		return types.Item{}, false, fmt.Errorf("docker: manifests of %s: %w", name, err)
	}
	if len(hits) == 0 {
		return types.Item{}, false, nil
	}
	return hits[0].Item, true, nil
}

// FolderManifest returns the manifest file in the image folder dir; ok is
// false when the folder holds none, and so is no tag.
func FolderManifest(
	ctx context.Context,
	items store.ItemStore,
	repoKey string,
	cached bool,
	dir string,
) (types.Item, bool, error) {
	for _, file := range []string{ManifestFile, ListManifestFile} {
		it, err := items.Get(ctx, repoKey, cached, dir+"/"+file)
		switch {
		case err == nil && !it.Folder:
			return it, true, nil
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return types.Item{}, false, fmt.Errorf("docker: get %s/%s: %w", dir, file, err)
		}
	}
	return types.Item{}, false, nil
}

// ensurePackage returns the image's package, creating it when absent with
// the time of the image's earliest manifest, so a reindex creates the same
// record that the first push did.
func ensurePackage(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	name string,
) (types.Package, error) {
	p, err := st.Packages().Find(ctx, c.Key, name)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return types.Package{}, fmt.Errorf("docker: package %s: %w", name, err)
	}
	first, _, err := firstManifest(ctx, st.Items(), c.Key, name)
	if err != nil {
		return types.Package{}, err
	}
	p, err = st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: name, Type: c.PackageType, Created: first.Created,
	})
	if err != nil {
		return types.Package{}, fmt.Errorf("docker: package %s: %w", name, err)
	}
	return p, nil
}

// tagVersion returns the version for the tag folder of the image name;
// ok is false when the folder is no tag in the index.
func tagVersion(
	ctx context.Context,
	st store.Store,
	repoKey, name, folder string,
) (types.PackageVersion, bool, error) {
	p, err := st.Packages().Find(ctx, repoKey, name)
	if errors.Is(err, store.ErrNotFound) {
		return types.PackageVersion{}, false, nil
	}
	if err != nil {
		return types.PackageVersion{}, false, fmt.Errorf("docker: package %s: %w", name, err)
	}
	pv, err := st.Packages().Version(ctx, p.ID, folder)
	if errors.Is(err, store.ErrNotFound) {
		return types.PackageVersion{}, false, nil
	}
	if err != nil {
		return types.PackageVersion{}, false,
			fmt.Errorf("docker: version %s of %s: %w", folder, name, err)
	}
	return pv, true, nil
}

func link(ctx context.Context, st store.Store, pv types.PackageVersion, it types.Item) error {
	if err := st.Packages().LinkFile(ctx, types.VersionFile{
		VersionID: pv.ID, ItemID: it.ID,
	}); err != nil {
		return fmt.Errorf("docker: link %s: %w", it.FullPath(), err)
	}
	return nil
}

// syncTag makes the folder of the image name a version exactly while it
// holds a manifest: the version's time is the manifest's push, and its
// files are every file in the folder.
func syncTag(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	cached bool,
	name, folder string,
) error {
	dir := name + "/" + folder
	m, ok, err := FolderManifest(ctx, st.Items(), c.Key, cached, dir)
	if err != nil {
		return err
	}
	if !ok {
		return dropTag(ctx, st, c.Key, name, folder)
	}
	p, err := ensurePackage(ctx, st, c, name)
	if err != nil {
		return err
	}
	at := PushTime(m)
	pv, err := st.Packages().Version(ctx, p.ID, folder)
	switch {
	case errors.Is(err, store.ErrNotFound):
		pv, err = st.Packages().PutVersion(ctx, types.PackageVersion{
			PackageID: p.ID, Version: folder, Created: at,
		})
	case err == nil && !pv.Created.Equal(at):
		pv.Created = at
		pv, err = st.Packages().PutVersion(ctx, pv)
	}
	if err != nil {
		return fmt.Errorf("docker: version %s of %s: %w", folder, name, err)
	}
	return linkFolder(ctx, st, pv, c.Key, cached, dir)
}

// linkFolder makes the version's files exactly the files in dir. Every
// write keeps the links exact, since an item's deletion drops its links;
// a link to anything else is damage a reindex repairs by recreating the
// version with its dates, statistics and metadata, as the store cannot
// unlink one file.
func linkFolder(
	ctx context.Context,
	st store.Store,
	pv types.PackageVersion,
	repoKey string,
	cached bool,
	dir string,
) error {
	var want []types.Item
	for page := (store.Page{Limit: pageSize}); ; {
		children, next, err := st.Items().Children(ctx, repoKey, cached, dir, page)
		if err != nil {
			return fmt.Errorf("docker: list %s: %w", dir, err)
		}
		for _, f := range children {
			if !f.Folder {
				want = append(want, f)
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	linked, err := st.Packages().Files(ctx, pv.ID)
	if err != nil {
		return fmt.Errorf("docker: files of %s: %w", dir, err)
	}
	in := map[int64]bool{}
	for _, f := range want {
		in[f.ID] = true
	}
	if slices.ContainsFunc(linked, func(id int64) bool { return !in[id] }) {
		if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
			return fmt.Errorf("docker: relink %s: %w", dir, err)
		}
		if pv, err = st.Packages().PutVersion(ctx, pv); err != nil {
			return fmt.Errorf("docker: relink %s: %w", dir, err)
		}
	}
	for _, f := range want {
		if err := link(ctx, st, pv, f); err != nil {
			return err
		}
	}
	return nil
}

// dropTag removes the version of a folder that no longer holds a manifest,
// with its remaining file links.
func dropTag(ctx context.Context, st store.Store, repoKey, name, folder string) error {
	pv, ok, err := tagVersion(ctx, st, repoKey, name, folder)
	if err != nil || !ok {
		return err
	}
	if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
		return fmt.Errorf("docker: delete version %s of %s: %w", folder, name, err)
	}
	return nil
}

// syncPackage keeps the image a package exactly while it holds a manifest.
func syncPackage(ctx context.Context, st store.Store, c repo.Config, name string) error {
	_, has, err := firstManifest(ctx, st.Items(), c.Key, name)
	if err != nil {
		return err
	}
	if has {
		_, err := ensurePackage(ctx, st, c, name)
		return err
	}
	p, err := st.Packages().Find(ctx, c.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err == nil {
		err = st.Packages().Delete(ctx, p.ID)
	}
	if err != nil {
		return fmt.Errorf("docker: drop package %s: %w", name, err)
	}
	return nil
}

// affected collects the tag folders and images a removal touched.
type affected struct {
	cached bool
	// tags maps name/folder to the image and its folder.
	tags   map[string][2]string
	images map[string]bool
}

func (a *affected) add(it types.Item) {
	name, folder, ok := imageFolder(it)
	if !ok || folder == UploadsFolder {
		return
	}
	if a.images == nil {
		a.tags, a.images, a.cached = map[string][2]string{}, map[string]bool{}, it.Cached
	}
	a.images[name] = true
	if !IsDigestFolder(folder) {
		a.tags[name+"/"+folder] = [2]string{name, folder}
	}
}

// sync settles every affected tag, then every affected image, in path
// order so the index changes the same way on every store.
func (a affected) sync(ctx context.Context, st store.Store, c repo.Config) error {
	for _, k := range slices.Sorted(maps.Keys(a.tags)) {
		t := a.tags[k]
		if err := syncTag(ctx, st, c, a.cached, t[0], t[1]); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(a.images)) {
		if err := syncPackage(ctx, st, c, name); err != nil {
			return err
		}
	}
	return nil
}

// Reindex brings the package index of the local image repository c back in
// line with its item tree, which stays the source of truth (decision 0018):
// packages and versions the tree no longer backs go, every image holding a
// manifest is a package and every tag folder a version with exactly its
// folder's files. Records the tree still backs keep their dates and
// download statistics, so after the hooks' own maintenance nothing
// changes; an image indexed afresh is dated by its earliest manifest.
func Reindex(ctx context.Context, st store.Store, c repo.Config) error {
	err := st.Tx(ctx, func(ctx context.Context) error {
		hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
			aql.RepoSet{Refs: []aql.RepoRef{{Key: c.Key}}},
			&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
			aql.Or{
				&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ManifestFile},
				&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ListManifestFile},
			},
		}}, aql.Options{})
		if err != nil {
			return fmt.Errorf("docker: manifests of %s: %w", c.Key, err)
		}
		var a affected
		for _, h := range hits {
			a.add(h.Item)
		}
		if err := a.dropUnbacked(ctx, st, c.Key); err != nil {
			return err
		}
		return a.sync(ctx, st, c)
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// dropUnbacked removes the repository's packages that are no image of a,
// and the versions that are no tag of a.
func (a affected) dropUnbacked(ctx context.Context, st store.Store, repoKey string) error {
	for page := (store.Page{Limit: pageSize}); ; {
		pkgs, next, err := st.Packages().List(ctx, repoKey, page)
		if err != nil {
			return fmt.Errorf("docker: packages of %s: %w", repoKey, err)
		}
		for _, p := range pkgs {
			if err := a.dropPackage(ctx, st, p); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

func (a affected) dropPackage(ctx context.Context, st store.Store, p types.Package) error {
	if !a.images[p.Name] {
		if err := st.Packages().Delete(ctx, p.ID); err != nil {
			return fmt.Errorf("docker: drop package %s: %w", p.Name, err)
		}
		return nil
	}
	vs, err := st.Packages().Versions(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("docker: versions of %s: %w", p.Name, err)
	}
	for _, v := range vs {
		if _, ok := a.tags[p.Name+"/"+v.Version]; ok {
			continue
		}
		if err := st.Packages().DeleteVersion(ctx, v.ID); err != nil {
			return fmt.Errorf("docker: drop version %s of %s: %w", v.Version, p.Name, err)
		}
	}
	return nil
}
