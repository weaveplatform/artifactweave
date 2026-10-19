package golang

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Serves reports whether t is the Go module type.
func Serves(t repo.PackageType) bool { return t == repo.Go }

// Handler is the format of Go repositories, which the item service calls
// on every write and delete in them, whichever API carried it. It keeps the
// package index of decision 0035. A module is a package, and a version is a
// version while any of its .zip, .mod and .info is stored. The version
// links exactly those files, is dated by the first of them stored, and
// keeps the Time its stored .info gives as its metadata. A file whose path
// fails the path checks belongs to no version.
type Handler struct {
	// The generic format admits writes under every package type's deploy
	// policy.
	generic.Handler
}

// Deployed indexes a version file: it settles the file's version from the
// tree.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	f, ok := fileOf(it)
	if !ok {
		return nil
	}
	return syncVersion(ctx, env, c, it.Cached, f.Module, f.Version)
}

// Deleting runs before a file is removed. The item service drops the
// file's version link with it, and Deleted settles the rest.
func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

// Deleted settles each version a delete or a move took files from. A
// version that keeps a file may now date from a later one. A version left
// with none is gone already: the item service pruned it, and its module if
// that was the module's last version.
func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	type version struct {
		module, version string
		cached          bool
	}
	touched := map[version]bool{}
	for _, it := range removed {
		if f, ok := fileOf(it); ok {
			touched[version{f.Module, f.Version, it.Cached}] = true
		}
	}
	// In path order, so the index changes the same way on every store.
	for _, v := range slices.SortedFunc(maps.Keys(touched), func(a, b version) int {
		return cmp.Or(cmp.Compare(a.module, b.module), cmp.Compare(a.version, b.version))
	}) {
		if err := syncVersion(ctx, env, c, v.cached, v.module, v.version); err != nil {
			return err
		}
	}
	return nil
}

// Admit applies the deploy policy of every package type, then the
// republishing rule of decision 0035 to every write that lands content: a
// deploy by any API, and the target of a copy or a move. go.sum and the
// checksum database pin a version's .zip and .mod forever, so different
// content over a stored one is refused, even for a caller who may delete,
// while the same content and any .info pass.
func (h Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := h.Handler.Admit(ctx, env, c, w)
	if err != nil {
		return w, err //nolint:wrapcheck // the generic policy's Rejection, answered as it is
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	f, ok := ParseFile(w.Path)
	if !ok || f.Ext == ExtInfo {
		return w, nil
	}
	old, err := env.Store.Items().Get(ctx, c.Key, w.Cached, w.Path)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return w, nil
	case err != nil:
		return w, fmt.Errorf("golang: get %s: %w", w.Path, err)
	case old.Folder || old.Actual.SHA256 == w.Checksums.SHA256:
		return w, nil
	}
	return w, Republished(f)
}

// Republished is the refusal of different content over f, a stored .zip or
// .mod, whichever API carried it.
func Republished(f File) *format.Rejection {
	return format.Reject(http.StatusConflict,
		"Version %s of module %s already has a different .%s; delete the version to replace it.",
		f.Version, f.Module, f.Ext)
}

// Reindex rebuilds the repository's Go index from its tree in one
// transaction: every file whose path passes the path checks joins its
// version.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	st := env.Store
	err := st.Tx(ctx, func(ctx context.Context) error {
		hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
			aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
			&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: "*/@v"},
		}}, aql.Options{})
		if err != nil {
			return fmt.Errorf("golang: version files of %s: %w", c.Key, err)
		}
		modules := map[string]map[string]*pkgindex.IndexedVersion{}
		for _, h := range hits {
			f, ok := fileOf(h.Item)
			if !ok {
				continue
			}
			if modules[f.Module] == nil {
				modules[f.Module] = map[string]*pkgindex.IndexedVersion{}
			}
			v := modules[f.Module][f.Version]
			if v == nil {
				v = &pkgindex.IndexedVersion{Version: f.Version}
				modules[f.Module][f.Version] = v
			}
			v.Files = append(v.Files, h.Item.ID)
			v.Created = earliest(v.Created, h.Item.Created)
			if f.Ext == ExtInfo {
				t, ok, err := storedTime(ctx, env.Blobs, h.Item)
				if err != nil {
					return err
				}
				v.Metadata = metadataOf(t, ok)
			}
		}
		want := make([]pkgindex.Indexed, 0, len(modules))
		for name, versions := range modules {
			p := pkgindex.Indexed{Name: name}
			for _, v := range versions {
				p.Versions = append(p.Versions, *v)
				p.Created = earliest(p.Created, v.Created)
			}
			want = append(want, p)
		}
		return pkgindex.Reconcile(ctx, st, c, want) //nolint:wrapcheck // pkgindex wraps its own
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// Classify reports a version's .info as metadata, since it may be replaced
// under the same path, and every other file, a .zip and a .mod included,
// as unique.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if f, ok := ParseFile(p); ok && f.Ext == ExtInfo {
		return format.ClassMetadata
	}
	return format.ClassUnique
}

// fileOf reads the version file an item is; ok is false for a folder and
// for a file whose path fails the path checks.
func fileOf(it types.Item) (File, bool) {
	if it.Folder {
		return File{}, false
	}
	return ParseFile(it.FullPath())
}

// syncVersion makes version v of module m in c what the tree says: linked
// to exactly the version files stored, dated by the first of them, and
// with the Time its stored .info gives. The same reading of the tree is
// what Reindex does, so the hooks and a reindex agree; files stored
// anywhere but where the index reads (pkgindex.Stored) are not indexed.
func syncVersion(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	cached bool,
	m, v string,
) error {
	if cached != pkgindex.Stored(c).Cached {
		return nil
	}
	st := env.Store
	var files []int64
	var created time.Time
	var meta []byte
	for _, ext := range []string{ExtZip, ExtMod, ExtInfo} {
		p := File{Module: m, Version: v, Ext: ext}.Path()
		it, err := st.Items().Get(ctx, c.Key, cached, p)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			return fmt.Errorf("golang: get %s: %w", p, err)
		case it.Folder:
			continue
		}
		files = append(files, it.ID)
		created = earliest(created, it.Created)
		if ext == ExtInfo {
			t, ok, err := storedTime(ctx, env.Blobs, it)
			if err != nil {
				return err
			}
			meta = metadataOf(t, ok)
		}
	}
	if len(files) == 0 {
		return nil
	}
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: m, Type: c.PackageType, Created: created,
	})
	if err != nil {
		return fmt.Errorf("golang: module %s: %w", m, err)
	}
	_, err = pkgindex.Sync(ctx, st, p, pkgindex.IndexedVersion{
		Version: v, Created: created, Metadata: meta, Files: files,
	})
	return err //nolint:wrapcheck // pkgindex wraps its own
}

// earliest is the earlier of a and b, where a zero a is no time yet.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}
