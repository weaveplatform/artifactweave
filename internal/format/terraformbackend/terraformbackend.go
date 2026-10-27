// Package terraformbackend is the format of Terraform backend repositories
// (decision 0041): the workspaces and states of Terraform's remote backend,
// kept as items under workspaces/<ws>/ as 7.x keeps them. Its hooks keep the
// package index, in which a workspace is a package and each state a version
// named by its epoch milliseconds, whose metadata answers the API without
// reading the content. The content itself is written only through the
// Terraform backend API (internal/service/terraformstate), which marks its
// writes with Writing: through the item routes a deploy, a folder create, a
// copy or move into the repository, and a change to the lock's properties
// are refused, so nothing passes the lock or the seal. Deletes are allowed,
// into the trash can, as for any item.
package terraformbackend

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Serves reports whether t is the Terraform backend's package type.
func Serves(t repo.PackageType) bool { return t == repo.TerraformBackend }

// Settings are the format's server-wide options.
type Settings struct {
	// Keys opens sealed states, so the index holds their serial and
	// lineage. Without it a sealed state is indexed as unreadable.
	Keys *crypt.Keyring
}

// Handler is the Terraform backend format.
type Handler struct {
	Settings Settings
}

type writingKey struct{}

// Writing marks ctx as a write of the Terraform backend API, which Admit
// and the other checks let through: the workspace record, a state, the
// move of the current state to its timestamped name, and the lock's
// properties. state, when not nil, is what the API knows of the state the
// write stores, which Inspect hands to the hooks so they need not read the
// content back. Only server code reaches it: a client's request never
// carries it, whatever route it takes.
func Writing(ctx context.Context, state *Meta) context.Context {
	return context.WithValue(ctx, writingKey{}, writing{state: state})
}

type writing struct{ state *Meta }

func writingOf(ctx context.Context) (writing, bool) {
	w, ok := ctx.Value(writingKey{}).(writing)
	return w, ok
}

// Refusal is the answer to a write to repository c through the item routes.
// [I] 7.x's answer is unknown (research 32 section 6, item 11).
func Refusal(c repo.Config) *format.Rejection {
	return format.Rejectf(http.StatusBadRequest,
		"Repository '%s' is a Terraform backend repository; "+
			"its content is written only through the Terraform backend API", c.Key)
}

// Admit refuses every file write but the API's own: a deploy, a checksum
// deploy or sidecar, and the target of a copy or move. Such a write would
// pass the lock and the seal, and could replace state.latest.json under a
// running apply. The include and exclude patterns and blackedOut do not
// apply to the API's writes (decision 0041, "The item routes").
func (Handler) Admit(
	ctx context.Context,
	_ format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if _, ok := writingOf(ctx); ok {
		return w, nil
	}
	return w, Refusal(c)
}

// AdmitFolder refuses a folder create but the API's own, as Admit refuses
// a file.
func (Handler) AdmitFolder(ctx context.Context, _ format.Env, c repo.Config, _ string) error {
	if _, ok := writingOf(ctx); ok {
		return nil
	}
	return Refusal(c)
}

// AdmitAnnotation refuses a change to the lock's properties but the API's
// own, so the lock has one writer: force-unlock is the way to clear a lock
// left behind (decision 0041, "Locking") [I].
func (Handler) AdmitAnnotation(
	ctx context.Context,
	_ format.Env,
	c repo.Config,
	_ types.Item,
	a format.Annotation,
) error {
	if _, ok := writingOf(ctx); ok {
		return nil
	}
	for _, k := range []string{PropLockedBy, PropLockedAt} {
		if _, set := a.Set[k]; set || slices.Contains(a.Removed, k) {
			return format.Rejectf(http.StatusBadRequest,
				"The property '%s' in repository '%s' is a Terraform workspace's lock; "+
					"it changes only when the workspace is locked or unlocked", k, c.Key)
		}
	}
	return nil
}

// Inspect hands the hooks what the API knows of the state it writes. It
// reads nothing: any other write is refused by Admit, and a hook reads
// what it is not handed.
func (Handler) Inspect(
	ctx context.Context,
	_ format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	wr, ok := writingOf(ctx)
	if !ok || wr.state == nil || wr.state.Stored != w.Checksums.SHA256 {
		return nil, nil //nolint:nilnil // nothing known is no failure
	}
	m := *wr.state
	return &m, nil
}

// Deployed indexes a state file landed by the API, a move or a restore. A
// file whose content changed in place may have named another version, as
// a restore of an earlier state.latest.json does; that version is settled
// too.
func (h Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	f, ok := FileOf(it)
	if !ok || it.Cached != pkgindex.Stored(c).Cached {
		return nil
	}
	was, linked, err := pkgindex.VersionOf(ctx, env.Store, it.ID)
	if err != nil {
		return err //nolint:wrapcheck // pkgindex wraps its own
	}
	known, _ := env.Inspection.(*Meta)
	if err := h.sync(ctx, env, c, f.Workspace, f.Ms, known); err != nil {
		return err
	}
	if linked && was.Version != Version(f.Ms) {
		ms, err := strconv.ParseInt(was.Version, 10, 64)
		if err != nil {
			return fmt.Errorf(
				"terraformbackend: version %q of %s: %w",
				was.Version,
				it.FullPath(),
				err,
			)
		}
		return h.sync(ctx, env, c, f.Workspace, ms, nil)
	}
	return nil
}

// Deleting runs before a file is removed; the item service unlinks it.
func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

// Deleted settles each version a delete or a move took a state file from:
// one whose current and timestamped files named the same version keeps
// the other. A version left with none is gone already: the item service
// pruned it.
func (h Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	type version struct {
		ws string
		ms int64
	}
	touched := map[version]bool{}
	for _, it := range removed {
		if f, ok := FileOf(it); ok && it.Cached == pkgindex.Stored(c).Cached {
			touched[version{f.Workspace, f.Ms}] = true
		}
	}
	// In order, so the index changes the same way on every store.
	for _, v := range slices.SortedFunc(maps.Keys(touched), func(a, b version) int {
		return cmp.Or(cmp.Compare(a.ws, b.ws), cmp.Compare(a.ms, b.ms))
	}) {
		if err := h.sync(ctx, env, c, v.ws, v.ms, nil); err != nil {
			return err
		}
	}
	return nil
}

// Classify reports the current state and the workspace record as
// metadata, which change under the same path, and every earlier state as
// unique.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if f, ok := ParseFile(p); ok && !f.Latest {
		return format.ClassUnique
	}
	return format.ClassMetadata
}

// Reindex rebuilds the repository's index from its tree in one
// transaction, reading every state, unsealed, for its serial and lineage.
func (h Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	st := env.Store
	err := st.Tx(ctx, func(ctx context.Context) error {
		hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
			aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
			&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: Root + "/*"},
		}}, aql.Options{})
		if err != nil {
			return fmt.Errorf("terraformbackend: states of %s: %w", c.Key, err)
		}
		files := map[string]map[int64][]types.Item{}
		for _, hit := range hits {
			f, ok := FileOf(hit.Item)
			if !ok {
				continue
			}
			if files[f.Workspace] == nil {
				files[f.Workspace] = map[int64][]types.Item{}
			}
			files[f.Workspace][f.Ms] = append(files[f.Workspace][f.Ms], hit.Item)
		}
		want := make([]pkgindex.Indexed, 0, len(files))
		for ws, versions := range files {
			p := pkgindex.Indexed{Name: ws}
			for ms, its := range versions {
				// The current state describes the version when both name it,
				// as sync has it.
				slices.SortStableFunc(its, func(a, b types.Item) int {
					return cmp.Compare(rank(b), rank(a))
				})
				m, err := h.metaOf(ctx, env, c, ws, its[0], nil, nil)
				if err != nil {
					return err
				}
				v := pkgindex.IndexedVersion{
					Version: Version(ms), Created: Created(ms), Metadata: m.Encode(),
				}
				for _, it := range its {
					v.Files = append(v.Files, it.ID)
				}
				p.Versions = append(p.Versions, v)
				if p.Created.IsZero() || v.Created.Before(p.Created) {
					p.Created = v.Created
				}
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

// sync makes version ms of workspace ws what the tree says (decision 0041,
// "The package index"): linked to state.latest.json when the current state
// was created at ms, and to state.<ms>.json when it is stored, and
// described by the first of them. The same reading of the tree is
// Reindex's, so the hooks and a reindex agree. known is what the API
// handed the write, if anything.
func (h Handler) sync(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	ws string,
	ms int64,
	known *Meta,
) error {
	st := env.Store
	var files []types.Item
	latest, err := fileAt(ctx, st, c, LatestPath(ws))
	if err != nil {
		return err
	}
	if latest != nil && latest.Created.UnixMilli() == ms {
		files = append(files, *latest)
	}
	earlier, err := fileAt(ctx, st, c, StatePath(ws, ms))
	if err != nil {
		return err
	}
	if earlier != nil {
		files = append(files, *earlier)
	}
	pv, indexed, err := versionNamed(ctx, st, c, ws, Version(ms))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		if indexed {
			return drop(ctx, st, pv)
		}
		return nil
	}
	m, err := h.metaOf(ctx, env, c, ws, files[0], pv.Metadata, known)
	if err != nil {
		return err
	}
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Name: ws, Type: c.PackageType, Created: Created(ms),
	})
	if err != nil {
		return fmt.Errorf("terraformbackend: workspace %s: %w", ws, err)
	}
	v := pkgindex.IndexedVersion{Version: Version(ms), Created: Created(ms), Metadata: m.Encode()}
	for _, f := range files {
		v.Files = append(v.Files, f.ID)
	}
	_, err = pkgindex.Sync(ctx, st, p, v)
	return err //nolint:wrapcheck // pkgindex wraps its own
}

// metaOf is the Meta of it, a state file of workspace ws: the version's
// cached metadata or what the API handed the write when either was read
// from its content, and otherwise a reading of the content.
func (h Handler) metaOf(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	ws string,
	it types.Item,
	cached []byte,
	known *Meta,
) (Meta, error) {
	if m, ok := DecodeMeta(cached); ok && m.Stored == it.Actual.SHA256 {
		return m, nil
	}
	if known != nil && known.Stored == it.Actual.SHA256 {
		return *known, nil
	}
	stored, err := ReadStored(ctx, env.Blobs, it.Actual.SHA256)
	if err != nil {
		return Meta{}, err
	}
	return MetaOf(h.Settings.Keys, c.Key, ws, stored), nil
}

// fileAt is the file at p in c, or nil when there is none.
func fileAt(ctx context.Context, st store.Store, c repo.Config, p string) (*types.Item, error) {
	it, err := st.Items().Get(ctx, c.Key, pkgindex.Stored(c).Cached, p)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && it.Folder:
		return nil, nil //nolint:nilnil // nothing there is no failure
	case err != nil:
		return nil, fmt.Errorf("terraformbackend: get %s: %w", p, err)
	}
	return &it, nil
}

// versionNamed is version v of workspace ws's package, if the index has it.
func versionNamed(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	ws, v string,
) (types.PackageVersion, bool, error) {
	p, err := st.Packages().Find(ctx, c.Key, ws)
	if errors.Is(err, store.ErrNotFound) {
		return types.PackageVersion{}, false, nil
	}
	if err != nil {
		return types.PackageVersion{}, false, fmt.Errorf(
			"terraformbackend: workspace %s: %w",
			ws,
			err,
		)
	}
	pv, err := st.Packages().Version(ctx, p.ID, v)
	if errors.Is(err, store.ErrNotFound) {
		return types.PackageVersion{}, false, nil
	}
	if err != nil {
		return types.PackageVersion{}, false, fmt.Errorf(
			"terraformbackend: version %s of %s: %w", v, ws, err)
	}
	return pv, true, nil
}

// drop removes version pv, which no file names any more though one still
// links it, and its package when that was its last version.
func drop(ctx context.Context, st store.Store, pv types.PackageVersion) error {
	if err := st.Packages().DeleteVersion(ctx, pv.ID); err != nil {
		return fmt.Errorf("terraformbackend: drop version %s: %w", pv.Version, err)
	}
	rest, err := st.Packages().Versions(ctx, pv.PackageID)
	if err != nil {
		return fmt.Errorf("terraformbackend: versions of package %d: %w", pv.PackageID, err)
	}
	if len(rest) > 0 {
		return nil
	}
	if err := st.Packages().Delete(ctx, pv.PackageID); err != nil {
		return fmt.Errorf("terraformbackend: drop package %d: %w", pv.PackageID, err)
	}
	return nil
}

// rank orders a version's files: the current state before an earlier one.
func rank(it types.Item) int {
	if f, _ := FileOf(it); f.Latest {
		return 1
	}
	return 0
}
