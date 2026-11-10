package gems

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
)

// Handler is the RubyGems format's hooks: it admits a gem only when it
// reads as valid and its identity gives its path, keeps the generated
// index paths out of local and federated repositories, and indexes each
// stored gem as a version of its name. It embeds the generic hooks for
// deletes, which need nothing more: the item service drops a deleted gem's
// link and prunes a version left without files.
type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	pkg       Package
}

// Serves reports whether t is the RubyGems package type.
func Serves(t repo.PackageType) bool { return t == repo.Gems }

// Inspect reads and validates the stored gem of a write to a .gem path
// before the write's transaction, so Admit need not read it again. A
// checksum deploy and any other path inspect nothing. A write without
// content, or of a gem that does not read as valid or whose identity does
// not give its path, is refused with 400; a gem that cannot be opened
// fails the write.
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

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy. In a local or federated repository, a write to
// a generated index path (DerivedPath) is refused (409), since the protocol
// renders those from the stored gems. Any other write passes unless it is
// to a .gem path, which must be a valid gem path (400) holding a valid gem
// whose identity gives that path (400). It reuses Inspect's reading when it
// covers the same path and content, and reads the stored gem itself
// otherwise.
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
		return w, format.Rejectf(http.StatusConflict, "RubyGems index is generated from packages")
	}
	if !strings.HasSuffix(w.Path, ".gem") {
		return w, nil
	}
	if !ValidPath(w.Path) {
		return w, format.Rejectf(http.StatusBadRequest, "invalid RubyGems path")
	}
	if v, ok := env.Inspection.(inspection); !ok || v.sha != w.Checksums.SHA256 ||
		v.path != w.Path {
		if _, err := stored(ctx, env.Blobs, w.Checksums.SHA256, w.Path); err != nil {
			return w, err
		}
	}
	return w, nil
}

// Deployed links a stored gem at a valid gem path (ValidPath) to its
// version in the package index, creating the package and version when they
// are new, with the name and version read from the stored gem. Folders and
// files at other paths change nothing; a gem that no longer reads as valid
// fails with 400.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
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

// Reindex rebuilds the package index from the stored gems Files finds: a
// package per gem name, with a version per version string whose files are
// its gems for every platform and whose creation time is the earliest of
// theirs. Files at other paths are skipped, but a gem that does not read
// as valid fails the reindex with 400.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := Files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]model.Item{}
	for _, it := range items {
		if !ValidPath(it.FullPath()) {
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

// Classify classes the generated index paths (DerivedPath) as derived, so
// replication and federation skip them and each side renders its own, and
// every other path as unique content.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if DerivedPath(p) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

// DerivedPath reports whether p is one of the index paths the protocol
// generates: names, versions, specs.4.8.gz, latest_specs.4.8.gz,
// prerelease_specs.4.8.gz, or a path under info/ or quick/Marshal.4.8/.
func DerivedPath(p string) bool {
	return p == "names" || p == "versions" || p == "specs.4.8.gz" || p == "latest_specs.4.8.gz" ||
		p == "prerelease_specs.4.8.gz" ||
		strings.HasPrefix(p, "info/") ||
		strings.HasPrefix(p, "quick/Marshal.4.8/")
}

func stored(ctx context.Context, blobs format.Blobs, sha, path string) (Package, error) {
	if sha == "" {
		return Package{}, format.Rejectf(http.StatusBadRequest, "gem content is missing")
	}
	r, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Package{}, fmt.Errorf("gems: blob open: %w", err)
	}
	defer r.Close()
	p, err := Read(ctx, r, info.Size, path)
	if err != nil {
		return Package{}, format.Rejectf(http.StatusBadRequest, "invalid RubyGem: %v", err)
	}
	return p, nil
}

// Files lists every file, gem or not, in the tree the package index reads
// (pkgindex.Stored: a remote repository's cache, and any other
// repository's own items), through one AQL search of the store.
func Files(ctx context.Context, st format.Store, c repo.Config) ([]model.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("gems: list files: %w", err)
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
