package hex

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
)

// Handler is the format of Hex repositories, which the item service calls
// on every write and delete in them, whichever API carried it. It admits
// only package tarballs, each at the path its own metadata gives, and keeps
// the package index of each name's versions. The registry resources
// (names, versions, packages/NAME and public_key) are generated and signed
// by the protocol on each read, so no deploy may supply them.
type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	pkg       Package
}

// Inspect reads a tarball write's content before the write's transaction,
// so Admit decides without parsing under the write lock. It answers nil
// for a checksum write and for any path that is not a tarball path. A
// tarball that does not validate, or whose name and version do not give
// its path, is refused with 400; a blob that cannot be opened is an error.
func (Handler) Inspect(
	ctx context.Context, env format.Env, _ repo.Config, w format.Write,
) (any, error) {
	if !IsTarballPath(w.Path) || w.Op == format.OpChecksum {
		return nil, nil //nolint:nilnil // A non-package write has nothing to inspect.
	}
	p, err := stored(ctx, env.Blobs, w.Checksums.SHA256, w.Path)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, pkg: p}, nil
}

// Admit refuses every write to a federated repository (400), since Hex has
// no federated class, then applies the generic deploy policy, and passes a
// checksum write. Any other write must be a tarball at its canonical
// path: a write of a registry resource that is not cached is refused with
// 409, and any other path, or a tarball whose name and version do not give
// its path, with 400. It reuses Inspect's result for the same content at
// the same path, and reads the tarball again otherwise.
func (Handler) Admit(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) (format.Write, error) {
	if c.Class == repo.Federated {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"Hex does not support federated repositories",
		)
	}
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("hex: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && DerivedPath(w.Path) {
		return w, format.Rejectf(
			http.StatusConflict,
			"Hex registry metadata is generated from packages",
		)
	}
	if !IsTarballPath(w.Path) {
		return w, format.Rejectf(http.StatusBadRequest, "invalid Hex package path")
	}
	var p Package
	if in, ok := env.Inspection.(inspection); ok &&
		in.sha == w.Checksums.SHA256 && in.path == w.Path {
		p = in.pkg
	} else {
		p, err = stored(ctx, env.Blobs, w.Checksums.SHA256, w.Path)
		if err != nil {
			return w, err
		}
	}
	canonical, _ := TarballPath(p.Name, p.Version)
	if canonical != w.Path {
		return w, format.Rejectf(http.StatusBadRequest, "Hex package identity does not match path")
	}
	return w, nil
}

// Deployed reads a stored tarball again and links its name and version
// into the package index. Folders and other paths are not indexed. A
// tarball that no longer validates at its path fails the hook, which rolls
// the change back.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || !IsTarballPath(it.FullPath()) {
		return nil
	}
	p, err := stored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
	if err != nil {
		return err
	}
	if err := pkgindex.Link(ctx, env.Store, pkgindex.Version{
		RepoKey: c.Key, PackageType: c.PackageType, Package: p.Name, Version: p.Version,
	}, it.ID, it.Created); err != nil {
		return fmt.Errorf("hex: link package index: %w", err)
	}
	return nil
}

// Reindex rebuilds the package index of repository c from its stored
// tarballs (Files), reading each again: a version's files are the tarballs
// of its name and version, and each version and package takes its oldest
// file's creation time. A stored tarball that no longer validates fails
// the reindex instead of being dropped from the index.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := Files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]model.Item{}
	for _, it := range items {
		if !IsTarballPath(it.FullPath()) {
			continue
		}
		p, err := stored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
		if err != nil {
			return err
		}
		if by[p.Name] == nil {
			by[p.Name] = map[string][]model.Item{}
		}
		by[p.Name][p.Version] = append(by[p.Name][p.Version], it)
	}
	wanted := make([]pkgindex.Indexed, 0, len(by))
	for name, versions := range by {
		indexed := pkgindex.Indexed{Name: name}
		for version, its := range versions {
			v := pkgindex.IndexedVersion{Version: version}
			for _, it := range its {
				v.Files = append(v.Files, it.ID)
				if v.Created.IsZero() || it.Created.Before(v.Created) {
					v.Created = it.Created
				}
			}
			slices.Sort(v.Files)
			indexed.Versions = append(indexed.Versions, v)
			if indexed.Created.IsZero() || v.Created.Before(indexed.Created) {
				indexed.Created = v.Created
			}
		}
		slices.SortFunc(indexed.Versions, func(a, b pkgindex.IndexedVersion) int {
			return strings.Compare(a.Version, b.Version)
		})
		wanted = append(wanted, indexed)
	}
	slices.SortFunc(wanted, func(a, b pkgindex.Indexed) int {
		return strings.Compare(a.Name, b.Name)
	})
	if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
		return fmt.Errorf("hex: reconcile package index: %w", err)
	}
	return nil
}

// Classify reports the registry resources as derived, since every
// repository generates and signs its own, and every other path as unique.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if DerivedPath(p) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

// DerivedPath reports whether p is a registry resource the protocol
// generates: names, versions, public_key or anything under packages/.
func DerivedPath(p string) bool {
	return p == "names" || p == "versions" || p == "public_key" ||
		strings.HasPrefix(p, "packages/")
}

func stored(ctx context.Context, blobs format.Blobs, sha, p string) (Package, error) {
	if sha == "" || !IsTarballPath(p) {
		return Package{}, format.Rejectf(http.StatusBadRequest, "invalid Hex package")
	}
	r, _, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("hex: open blob: %w", err)
	}
	defer func() { _ = r.Close() }()
	pkg, err := InspectTarball(r)
	if err != nil {
		return Package{}, format.Rejectf(http.StatusBadRequest, "invalid Hex tarball")
	}
	canonical, _ := TarballPath(pkg.Name, pkg.Version)
	if p != canonical {
		return Package{}, format.Rejectf(
			http.StatusBadRequest,
			"Hex package identity does not match path",
		)
	}
	return pkg, nil
}

// Files lists the files repository c's package index reads
// (pkgindex.Stored): a remote's cached files, or a hosted repository's
// own, in no promised order. A search failure is returned wrapped.
func Files(ctx context.Context, st format.Store, c repo.Config) ([]model.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("hex: list files: %w", err)
	}
	out := make([]model.Item, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Item)
	}
	return out, nil
}

var (
	_ format.Handler    = Handler{}
	_ format.Inspector  = Handler{}
	_ format.Admitter   = Handler{}
	_ format.Reindexer  = Handler{}
	_ format.Classifier = Handler{}
)
