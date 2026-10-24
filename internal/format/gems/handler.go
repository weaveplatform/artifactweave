package gems

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

func Serves(t repo.PackageType) bool { return t == repo.Gems }

func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if !strings.HasSuffix(w.Path, ".gem") || w.Op == format.OpChecksum {
		return nil, nil //nolint:nilnil // No inspection is needed for non-gem and checksum writes.
	}
	p, err := stored(ctx, env.Blobs, w.Checksums.SHA256, w.Path)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, pkg: p}, nil
}

func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("gems: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && (c.Class == repo.Local || c.Class == repo.Federated) && DerivedPath(w.Path) {
		return w, format.Reject(http.StatusConflict, "RubyGems index is generated from packages")
	}
	if !strings.HasSuffix(w.Path, ".gem") {
		return w, nil
	}
	if !ValidPath(w.Path) {
		return w, format.Reject(http.StatusBadRequest, "invalid RubyGems path")
	}
	if v, ok := env.Inspection.(inspection); !ok || v.sha != w.Checksums.SHA256 ||
		v.path != w.Path {
		if _, err := stored(ctx, env.Blobs, w.Checksums.SHA256, w.Path); err != nil {
			return w, err
		}
	}
	return w, nil
}

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || !ValidPath(it.FullPath()) {
		return nil
	}
	p, err := stored(ctx, env.Blobs, it.Actual.SHA256, it.FullPath())
	if err != nil {
		return err
	}
	if err := pkgindex.Link(ctx, env.Store, pkgindex.Version{
		RepoKey: c.Key, PackageType: c.PackageType, Package: p.Name, Version: p.Version,
	}, it.ID, it.Created); err != nil {
		return fmt.Errorf("gems: link package index: %w", err)
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
		if !ValidPath(it.FullPath()) {
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
		slices.SortFunc(
			indexed.Versions,
			func(a, b pkgindex.IndexedVersion) int { return strings.Compare(a.Version, b.Version) },
		)
		wanted = append(wanted, indexed)
	}
	slices.SortFunc(
		wanted,
		func(a, b pkgindex.Indexed) int { return strings.Compare(a.Name, b.Name) },
	)
	return pkgindex.Reconcile(ctx, env.Store, c, wanted) //nolint:wrapcheck
}

func (Handler) Classify(_ repo.Config, p string) format.Class {
	if DerivedPath(p) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

func DerivedPath(p string) bool {
	return p == "names" || p == "versions" || p == "specs.4.8.gz" || p == "latest_specs.4.8.gz" ||
		p == "prerelease_specs.4.8.gz" ||
		strings.HasPrefix(p, "info/") ||
		strings.HasPrefix(p, "quick/Marshal.4.8/")
}

func stored(ctx context.Context, blobs format.Blobs, sha, path string) (Package, error) {
	if sha == "" {
		return Package{}, format.Reject(http.StatusBadRequest, "gem content is missing")
	}
	r, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("gems: blob open: %w", err)
	}
	defer r.Close()
	p, err := Read(ctx, r, info.Size, path)
	if err != nil {
		return Package{}, format.Reject(http.StatusBadRequest, "invalid RubyGem: %v", err)
	}
	return p, nil
}

func Files(ctx context.Context, st store.Store, c repo.Config) ([]types.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("gems: list files: %w", err)
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
