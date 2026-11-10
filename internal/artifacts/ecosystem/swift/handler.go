package swift

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Handler keeps the package index derived from immutable source archives.
// Every write path, including ordinary item PUT, copy and restore, passes
// through these hooks so no malformed source is advertised to SwiftPM.
type Handler struct{ generic.Handler }

type inspection struct {
	path, sha string
}

// Inspect validates the stored archive of a write to a release archive
// path (ParseArchivePath) before the write's transaction, so Admit need not
// read it again. A checksum deploy and any other path inspect nothing. An
// invalid archive is refused with 422; one that cannot be opened fails the
// write.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum {
		return inspection{}, nil
	}
	if _, ok := ParseArchivePath(w.Path); !ok {
		return inspection{}, nil
	}
	rc, _, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, fmt.Errorf("swift: open source archive: %w", err)
	}
	defer rc.Close()
	if _, err := InspectArchive(ctx, rc, w.Size); err != nil {
		return nil, format.Rejectf(http.StatusUnprocessableEntity,
			"Invalid Swift source archive: %v", err)
	}
	return inspection{path: w.Path, sha: w.Checksums.SHA256}, nil
}

// Admit applies the generic deploy policy, returning its refusal as it is,
// then passes a checksum deploy. Any other write must be at a canonical
// release path, <scope>/<name>/<name>-<version>.zip (400), with a valid
// archive (422), which it inspects itself unless Inspect covered the same
// path and content. A write of other content over a stored release is
// refused (409); the same content, or a folder at the path, passes.
func (h Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := h.Handler.Admit(ctx, env, c, w)
	if err != nil {
		return w, err //nolint:wrapcheck // preserve the generic policy's rejection
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	p, ok := ParseArchivePath(w.Path)
	if !ok || p.ArchivePath() != w.Path {
		return w, format.Rejectf(http.StatusBadRequest,
			"Swift releases must use <scope>/<name>/<name>-<version>.zip")
	}
	if got, ok := env.Inspection.(inspection); !ok || got.path != w.Path ||
		got.sha != w.Checksums.SHA256 {
		rc, _, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
		if err != nil {
			return w, fmt.Errorf("swift: open source archive: %w", err)
		}
		_, checkErr := InspectArchive(ctx, rc, w.Size)
		_ = rc.Close()
		if checkErr != nil {
			return w, format.Rejectf(http.StatusUnprocessableEntity,
				"Invalid Swift source archive: %v", checkErr)
		}
	}
	old, err := env.Store.Items().Get(ctx, c.Key, w.Cached, w.Path)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return w, nil
	case err != nil:
		return w, fmt.Errorf("swift: existing release: %w", err)
	case old.Folder || old.Actual.SHA256 == w.Checksums.SHA256:
		return w, nil
	default:
		return w, format.Rejectf(http.StatusConflict,
			"Swift release %s already exists with different content", p.Version)
	}
}

// Deployed links a stored release archive to its version in the package
// index, creating the package and version when they are new. Folders,
// files at other paths and items outside the tree the index reads (a
// remote repository's cache, or a local one's own items) change nothing.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || it.Cached != pkgindex.Stored(c).Cached {
		return nil
	}
	p, ok := ParseArchivePath(it.FullPath())
	if !ok {
		return nil
	}
	if err := pkgindex.Link(ctx, env.Store, pkgindex.Version{
		RepoKey: c.Key, PackageType: c.PackageType, Package: p.ID(), Version: p.Version,
	}, it.ID, it.Created); err != nil {
		return fmt.Errorf("swift: link deployed release: %w", err)
	}
	return nil
}

// Deleting does nothing: the item service drops a removed archive's
// version link with it.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted does nothing: a version is one archive, so the item service's
// pruning of versions left without files is all a delete needs.
func (Handler) Deleted(context.Context, format.Env, repo.Config, []model.Item) error { return nil }

// Classify classes every path as unique content, as the generic hooks do:
// a release archive is content only its publisher can supply again.
func (Handler) Classify(_ repo.Config, _ string) format.Class { return format.ClassUnique }

// Reindex restores the version index from current archives after delete,
// restore or import. Invalid legacy items are omitted, never advertised.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	hits, err := env.Store.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return fmt.Errorf("swift: search source archives: %w", err)
	}
	byName := map[string]*pkgindex.Indexed{}
	for _, hit := range hits {
		it := hit.Item
		p, ok := ParseArchivePath(it.FullPath())
		if !ok || it.Folder {
			continue
		}
		rc, _, err := env.Blobs.Open(ctx, it.Actual.SHA256)
		if err != nil {
			return fmt.Errorf("swift: open indexed source archive: %w", err)
		}
		_, inspectErr := InspectArchive(ctx, rc, it.Size)
		_ = rc.Close()
		if inspectErr != nil {
			continue
		}
		indexed := byName[p.ID()]
		if indexed == nil {
			indexed = &pkgindex.Indexed{Name: p.ID(), Created: it.Created}
			byName[p.ID()] = indexed
		}
		if it.Created.Before(indexed.Created) {
			indexed.Created = it.Created
		}
		indexed.Versions = append(indexed.Versions, pkgindex.IndexedVersion{
			Version: p.Version, Created: it.Created, Files: []int64{it.ID},
		})
	}
	wanted := make([]pkgindex.Indexed, 0, len(byName))
	for _, indexed := range byName {
		slices.SortFunc(indexed.Versions, func(a, b pkgindex.IndexedVersion) int {
			return cmp.Compare(a.Version, b.Version)
		})
		wanted = append(wanted, *indexed)
	}
	slices.SortFunc(wanted, func(a, b pkgindex.Indexed) int {
		return cmp.Compare(a.Name, b.Name)
	})
	if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
		return fmt.Errorf("swift: reconcile packages: %w", err)
	}
	return nil
}

var (
	_ format.Handler    = Handler{}
	_ format.Inspector  = Handler{}
	_ format.Admitter   = Handler{}
	_ format.Classifier = Handler{}
	_ format.Reindexer  = Handler{}
)
