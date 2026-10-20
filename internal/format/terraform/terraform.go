// Package terraform is the format of Terraform registry repositories
// (decision 0040): the modules and Terraform provider packages that users
// store in terraform repositories, which the module registry, the provider
// registry and the network mirror serve. It reads the paths of what is
// stored, keeps the package index, computes the h1: hashes of a Terraform
// provider package's archives, and makes the checks a registry download
// runs. The Terraform backend's state is decision 0041's, under a package
// type of its own.
//
// A repository's terraformType chooses what it indexes. A MODULE
// repository indexes each module zip at <ns>/<name>/<system>/<version>.zip
// as version <version> of package <ns>/<name>/<system>. A PROVIDER
// repository indexes the files of a Terraform provider package's release
// at [<folders>/]<ns>/<type>/<version>/ as version <version> of package
// <ns>/<type>: its archives, its SHA256SUMS, the SHA256SUMS's signature and
// its manifest. Any other file, a module zip in a PROVIDER repository and
// the reverse included, is stored and not indexed, and so is everything in
// a repository without terraformType. The format sets no property.
package terraform

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Serves reports whether t is the Terraform registry's package type. The
// backend's terraformbackend is another.
func Serves(t repo.PackageType) bool { return t == repo.Terraform }

// Handler is the Terraform registry format, which the item service calls
// on every write and delete in a terraform repository, whichever API
// carried it.
type Handler struct {
	// The generic format admits writes under every package type's deploy
	// policy: a file the index cannot use is stored all the same.
	generic.Handler
}

// Meta is the package index's metadata of a version of a Terraform
// provider package: its archives by platform, "<os>_<arch>". A module
// version has none.
type Meta struct {
	Archives map[string]Archive `json:"archives"`
}

// Archive is one archive of a version of a Terraform provider package.
type Archive struct {
	// Path is the archive's repository-relative path.
	Path string `json:"path"`
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// SHA256 is the content the hash is of. A later index of the version
	// reuses the hash while the archive keeps that content, so a release
	// deployed file by file reads each archive once.
	SHA256 string `json:"sha256"`
	// H1 is the archive's h1: hash, empty for content that is no zip.
	H1 string `json:"h1,omitempty"`
}

// ReadMeta reads a version's Meta.
func ReadMeta(b []byte) (Meta, error) {
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("terraform: version metadata: %w", err)
	}
	return m, nil
}

// inspection is what Inspect read of an archive written to a PROVIDER
// repository: the h1: hash of its content.
type inspection struct {
	sha256, h1 string
}

// Inspect hashes an archive of a Terraform provider package before the
// write's transaction, which then indexes it without decompressing
// anything. Other content needs no reading.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (any, error) {
	if c.TerraformType != repo.TerraformProvider || w.Checksums.SHA256 == "" {
		return nil, nil //nolint:nilnil // nothing to read is no failure
	}
	if f, ok := ParseProviderPackageFile(w.Path); !ok || f.Kind != KindArchive {
		return nil, nil //nolint:nilnil // nothing to read is no failure
	}
	h1, _, err := storedH1(ctx, env.Blobs, w.Checksums.SHA256)
	if err != nil {
		return nil, err
	}
	return &inspection{sha256: w.Checksums.SHA256, h1: h1}, nil
}

// Deployed settles the version of a file the repository's terraformType
// indexes, from the tree: a deploy may make the file its version's, or
// take that place from another path.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || it.Cached != pkgindex.Stored(c).Cached {
		return nil
	}
	switch c.TerraformType {
	case repo.TerraformModule:
		if m, ok := ParseModule(it.FullPath()); ok {
			return syncModule(ctx, env.Store, c, m)
		}
	case repo.TerraformProvider:
		if f, ok := ParseProviderPackageFile(it.FullPath()); ok {
			known := map[string]string{}
			if in, ok := env.Inspection.(*inspection); ok {
				known[in.sha256] = in.h1
			}
			return syncRelease(ctx, env, c, f.Package(), f.Version, known)
		}
	}
	return nil
}

// Deleting runs before a file is removed. The item service drops the
// file's version link with it, and Deleted settles the rest.
func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

// Deleted settles each version a delete or a move took files from: another
// path may now hold a place the removed file held, and a version left with
// none is gone already, as the item service pruned it.
func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	stored := pkgindex.Stored(c).Cached
	modules := map[string]Module{}
	releases := map[[2]string]bool{}
	for _, it := range removed {
		if it.Folder || it.Cached != stored {
			continue
		}
		switch c.TerraformType {
		case repo.TerraformModule:
			if m, ok := ParseModule(it.FullPath()); ok {
				m.V = false
				modules[m.Path()] = m
			}
		case repo.TerraformProvider:
			if f, ok := ParseProviderPackageFile(it.FullPath()); ok {
				releases[[2]string{f.Package(), f.Version}] = true
			}
		}
	}
	// In order, so the index changes the same way on every store.
	for _, k := range slices.Sorted(maps.Keys(modules)) {
		if err := syncModule(ctx, env.Store, c, modules[k]); err != nil {
			return err
		}
	}
	for _, k := range slices.SortedFunc(maps.Keys(releases), func(a, b [2]string) int {
		return cmp.Or(strings.Compare(a[0], b[0]), strings.Compare(a[1], b[1]))
	}) {
		if err := syncRelease(ctx, env, c, k[0], k[1], map[string]string{}); err != nil {
			return err
		}
	}
	return nil
}

// Reindex rebuilds the repository's index from its tree: the module index
// of a MODULE repository, the Terraform provider package index of a
// PROVIDER one, and none for a repository without terraformType. A PROVIDER
// repository's archives are hashed anew before the transaction that
// rebuilds the index, so no decompression holds the store's write lock;
// the transaction hashes only an archive stored in between.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	known := map[string]string{}
	if c.TerraformType == repo.TerraformProvider {
		if _, err := releasesWanted(ctx, env, c, known); err != nil {
			return err
		}
	}
	st := env.Store
	err := st.Tx(ctx, func(ctx context.Context) error {
		var want []pkgindex.Indexed
		var err error
		switch c.TerraformType {
		case repo.TerraformModule:
			want, err = modulesWanted(ctx, st, c)
		case repo.TerraformProvider:
			want, err = releasesWanted(ctx, env, c, known)
		}
		if err != nil {
			return err
		}
		return pkgindex.Reconcile(ctx, st, c, want) //nolint:wrapcheck // pkgindex wraps its own
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// syncModule makes version m.Version of m's module what the tree says:
// linked to the first of its two spellings stored, <version>.zip before
// v<version>.zip, and dated by it. Reindex reads the tree the same way.
func syncModule(ctx context.Context, st store.Store, c repo.Config, m Module) error {
	cached := pkgindex.Stored(c).Cached
	for _, v := range []bool{false, true} {
		m.V = v
		it, err := st.Items().Get(ctx, c.Key, cached, m.Path())
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			return fmt.Errorf("terraform: get %s: %w", m.Path(), err)
		case it.Folder:
			continue
		}
		return index(ctx, st, c, m.Package(), pkgindex.IndexedVersion{
			Version: m.Version, Created: it.Created, Files: []int64{it.ID},
		})
	}
	return nil
}

// index makes version v of package name in c what v says, creating the
// package when it is new.
func index(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	name string,
	v pkgindex.IndexedVersion,
) error {
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: name, Type: c.PackageType, Created: v.Created,
	})
	if err != nil {
		return fmt.Errorf("terraform: package %s: %w", name, err)
	}
	_, err = pkgindex.Sync(ctx, st, p, v)
	return err //nolint:wrapcheck // pkgindex wraps its own
}

// modulesWanted reads every module version from c's tree.
func modulesWanted(ctx context.Context, st store.Store, c repo.Config) ([]pkgindex.Indexed, error) {
	items, err := search(ctx, st, c,
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: "*.zip"},
		&aql.FieldCriterion{Field: aql.FieldDepth, Op: aql.OpEq, Number: 4},
	)
	if err != nil {
		return nil, err
	}
	holders := map[string]map[string]types.Item{}
	for _, it := range items {
		m, ok := ParseModule(it.FullPath())
		if !ok {
			continue
		}
		if holders[m.Package()] == nil {
			holders[m.Package()] = map[string]types.Item{}
		}
		if cur, ok := holders[m.Package()][m.Version]; !ok || it.FullPath() < cur.FullPath() {
			holders[m.Package()][m.Version] = it
		}
	}
	want := make([]pkgindex.Indexed, 0, len(holders))
	for name, versions := range holders {
		p := pkgindex.Indexed{Name: name}
		for v, it := range versions {
			p.Versions = append(p.Versions, pkgindex.IndexedVersion{
				Version: v, Created: it.Created, Files: []int64{it.ID},
			})
			p.Created = earliest(p.Created, it.Created)
		}
		want = append(want, p)
	}
	return want, nil
}

// held is a file of a Terraform provider package's version that holds its
// place in the version.
type held struct {
	f  ProviderPackageFile
	it types.Item
}

// syncRelease makes version v of the Terraform provider package name what
// the tree says: linked to the file that holds each place in it, dated by
// the first of them stored, and with the archives' hashes as its Meta. An
// archive's hash is known's for its content, else the version's stored
// Meta's, else read from the content. Reindex reads the tree the same way.
func syncRelease(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	name, v string,
	known map[string]string,
) error {
	items, err := search(ctx, env.Store, c,
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: releasePrefix + "*"},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: "*/" + v},
	)
	if err != nil {
		return err
	}
	places := holders(items)[name][v]
	if len(places) == 0 {
		return nil
	}
	if err := knownFromIndex(ctx, env.Store, c, name, v, known); err != nil {
		return err
	}
	iv, err := release(ctx, env.Blobs, v, places, known)
	if err != nil {
		return err
	}
	return index(ctx, env.Store, c, name, iv)
}

// knownFromIndex adds the hashes the stored Meta of version v of name
// records to known, where known has none for the content.
func knownFromIndex(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	name, v string,
	known map[string]string,
) error {
	p, err := st.Packages().Find(ctx, c.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("terraform: package %s: %w", name, err)
	}
	pv, err := st.Packages().Version(ctx, p.ID, v)
	if errors.Is(err, store.ErrNotFound) || err == nil && len(pv.Metadata) == 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("terraform: version %s of %s: %w", v, name, err)
	}
	meta, err := ReadMeta(pv.Metadata)
	if err != nil {
		return err
	}
	for _, a := range meta.Archives {
		if _, ok := known[a.SHA256]; !ok {
			known[a.SHA256] = a.H1
		}
	}
	return nil
}

// releasesWanted reads every version of every Terraform provider package
// from c's tree, hashing each archive whose content known has no hash for
// and adding it there.
func releasesWanted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	known map[string]string,
) ([]pkgindex.Indexed, error) {
	items, err := search(ctx, env.Store, c,
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: releasePrefix + "*"},
	)
	if err != nil {
		return nil, err
	}
	all := holders(items)
	want := make([]pkgindex.Indexed, 0, len(all))
	for name, versions := range all {
		p := pkgindex.Indexed{Name: name}
		for v, places := range versions {
			iv, err := release(ctx, env.Blobs, v, places, known)
			if err != nil {
				return nil, err
			}
			p.Versions = append(p.Versions, iv)
			p.Created = earliest(p.Created, iv.Created)
		}
		want = append(want, p)
	}
	return want, nil
}

// holders sorts the files of Terraform provider packages among items into
// their packages and versions, by the place each holds, keeping the path
// that sorts first where two claim one place.
func holders(items []types.Item) map[string]map[string]map[string]held {
	out := map[string]map[string]map[string]held{}
	for _, it := range items {
		f, ok := ParseProviderPackageFile(it.FullPath())
		if !ok {
			continue
		}
		if out[f.Package()] == nil {
			out[f.Package()] = map[string]map[string]held{}
		}
		places := out[f.Package()][f.Version]
		if places == nil {
			places = map[string]held{}
			out[f.Package()][f.Version] = places
		}
		if cur, ok := places[f.role()]; !ok || it.FullPath() < cur.it.FullPath() {
			places[f.role()] = held{f: f, it: it}
		}
	}
	return out
}

// release is version v as its places give it: their files, the earliest
// date among them, and the archives' hashes, from known or else read from
// the content and added to known.
func release(
	ctx context.Context,
	blobs format.Blobs,
	v string,
	places map[string]held,
	known map[string]string,
) (pkgindex.IndexedVersion, error) {
	iv := pkgindex.IndexedVersion{Version: v}
	meta := Meta{Archives: map[string]Archive{}}
	for _, role := range slices.Sorted(maps.Keys(places)) {
		h := places[role]
		iv.Files = append(iv.Files, h.it.ID)
		iv.Created = earliest(iv.Created, h.it.Created)
		if h.f.Kind != KindArchive {
			continue
		}
		sha := h.it.Actual.SHA256
		h1, ok := known[sha]
		if !ok {
			var err error
			if h1, _, err = storedH1(ctx, blobs, sha); err != nil {
				return iv, err
			}
			known[sha] = h1
		}
		meta.Archives[h.f.Platform()] = Archive{
			Path: h.it.FullPath(), OS: h.f.OS, Arch: h.f.Arch, SHA256: sha, H1: h1,
		}
	}
	if len(meta.Archives) > 0 {
		// A map of strings and plain structs always marshals.
		iv.Metadata, _ = json.Marshal(meta)
	}
	return iv, nil
}

// search returns the files of c, where its index reads (pkgindex.Stored),
// that meet more.
func search(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	more ...aql.Node,
) ([]types.Item, error) {
	criteria := aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: append(criteria, more...)},
		aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("terraform: files of %s: %w", c.Key, err)
	}
	out := make([]types.Item, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Item)
	}
	return out, nil
}

// earliest is the earlier of a and b, where a zero a is no time yet.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}
