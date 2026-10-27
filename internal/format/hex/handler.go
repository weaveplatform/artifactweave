package hex

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	pkg       Package
}

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

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
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

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := Files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]types.Item{}
	for _, it := range items {
		if !IsTarballPath(it.FullPath()) {
			continue
		}
		p, err := stored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
		if err != nil {
			return err
		}
		if by[p.Name] == nil {
			by[p.Name] = map[string][]types.Item{}
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

func (Handler) Classify(_ repo.Config, p string) format.Class {
	if DerivedPath(p) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

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

func Files(ctx context.Context, st store.Store, c repo.Config) ([]types.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("hex: list files: %w", err)
	}
	out := make([]types.Item, 0, len(hits))
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
