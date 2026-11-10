package luarocks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

const (
	// KindManifest is the work kind that rebuilds a repository's manifest
	// files, keyed by the repository. The persistent work queue stores it
	// with each item, so it never changes.
	KindManifest = "luarocks.manifest"
	// ManifestPath is the path of the plain manifest at the repository root;
	// the Lua version variants are manifest-5.1 to manifest-5.5.
	ManifestPath = "manifest"
	// PropName is the property that holds a file's package name. The items
	// keep this name and the other luarocks.* names below, so they never
	// change.
	PropName = "luarocks.name" // [I] exact 7.161.32 property spelling: #216
	// PropVersion is the property that holds a file's version, its revision
	// included.
	PropVersion = "luarocks.version"
	// PropRevision is the property that holds a file's revision.
	PropRevision = "luarocks.revision"
	// PropArch is the property that holds a rock's architecture, or
	// rockspec for a rockspec.
	PropArch = "luarocks.arch"
)

var rockName = regexp.MustCompile(
	`^([A-Za-z0-9_][A-Za-z0-9_.-]*)-([^-]+-[0-9]+)\.(rockspec|[A-Za-z0-9_-]+\.rock)$`,
)

// Identity is the package coordinate read from a flat LuaRocks filename.
type Identity struct{ Name, Version, Revision, Arch string }

// Parse accepts only the root layout the native client searches. The final
// -revision is part of LuaRocks' version, not a separate indexed version.
func Parse(path string) (Identity, bool) {
	parts := rockName.FindStringSubmatch(path)
	if len(parts) == 0 {
		return Identity{}, false
	}
	_, rev, _ := strings.Cut(parts[2], "-")
	arch := strings.TrimSuffix(parts[3], ".rock")
	return Identity{parts[1], parts[2], rev, arch}, true
}

// Serves reports whether t is the LuaRocks package type.
func Serves(t repo.PackageType) bool { return t == repo.LuaRocks }

// Handler is the LuaRocks format's hooks: it admits a rockspec or rock only
// at a valid root file name with a rockspec that matches it, indexes each
// stored file as a version of its package, keeps a file's .asc signature
// with it, and has the work queue derive the manifest files of local and
// federated repositories. It embeds the generic hooks.
type Handler struct{ generic.Handler }

type inspection struct {
	sha string
	id  Identity
}

// Inspect checks the stored content of a write to a valid root file name
// (Parse) before the write's transaction, so Admit need not check it
// again. A write to any other path, or without content, inspects nothing.
// Content that fails ValidateContent is refused (400, or 413 for a
// rockspec over 1 MiB); content that cannot be opened fails the write.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	id, ok := Parse(w.Path)
	if !ok || w.Checksums.SHA256 == "" {
		return struct{}{}, nil
	}
	if err := validateStored(ctx, env.Blobs, w.Checksums.SHA256, id); err != nil {
		return nil, err
	}
	return inspection{w.Checksums.SHA256, id}, nil
}

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy and a write to a remote's cache. Any other write
// of a .rockspec or .rock must use a valid root file name (400), and its
// content must pass ValidateContent; Admit reuses Inspect's check when it
// covers the same content and file name, and checks the stored content
// itself otherwise. Other files pass.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("luarocks: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum || w.Cached {
		return w, nil
	}
	if strings.HasSuffix(w.Path, ".rockspec") || strings.HasSuffix(w.Path, ".rock") {
		id, ok := Parse(w.Path)
		if !ok {
			return w, format.Rejectf(
				http.StatusBadRequest,
				"LuaRocks rocks and rockspecs must use a valid filename at the repository root",
			)
		}
		if in, ok := env.Inspection.(inspection); !ok || in.sha != w.Checksums.SHA256 ||
			in.id != id {
			if err := validateStored(ctx, env.Blobs, w.Checksums.SHA256, id); err != nil {
				return w, err
			}
		}
	}
	return w, nil
}

// Deployed indexes a stored file at a valid root file name in the tree the
// package index reads: it sets the file's luarocks.* properties, syncs its
// version from every stored file of that version, and queues the manifest
// work, as a stored manifest file does too. Folders and other items change
// nothing, and the work is queued only in a local or federated repository.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || it.Cached != pkgindex.Stored(c).Cached {
		return nil
	}
	id, ok := Parse(it.FullPath())
	if ok {
		if err := setProperties(ctx, env.Store, it, id); err != nil {
			return err
		}
		if err := syncVersion(ctx, env, c, id); err != nil {
			return err
		}
	}
	if ok || isManifest(it.FullPath()) {
		return queue(ctx, env, c, it.Cached)
	}
	return nil
}

// Deleting queues the manifest work when a rockspec, rock or manifest file
// of a local or federated repository is about to be deleted; folders and
// other files change nothing.
func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder {
		return nil
	}
	if _, ok := Parse(it.FullPath()); ok {
		return queue(ctx, env, c, it.Cached)
	}
	if isManifest(it.FullPath()) {
		return queue(ctx, env, c, it.Cached)
	}
	return nil
}

// Deleted syncs each version a delete touched from its files still
// stored. A version left without files needs nothing: the item service has
// already unlinked and pruned it.
func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []model.Item,
) error {
	seen := map[Identity]bool{}
	for _, it := range removed {
		id, ok := Parse(it.FullPath())
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		if err := syncVersion(ctx, env, c, id); err != nil {
			return err
		}
	}
	return nil
}

// Sidecars names the .asc signature that a rockspec or rock at a valid
// root file name owns, so the item service deletes, copies, moves and
// restores it with its owner; other files own none.
func (Handler) Sidecars(_ repo.Config, p string) []string {
	if _, ok := Parse(p); ok {
		return []string{p + ".asc"}
	}
	return nil
}

func derives(c repo.Config, cached bool) bool {
	return !cached && (c.Class == repo.Local || c.Class == repo.Federated)
}
func isManifest(p string) bool { return p == ManifestPath || strings.HasPrefix(p, "manifest-5.") }
func queue(ctx context.Context, env format.Env, c repo.Config, cached bool) error {
	if !derives(c, cached) {
		return nil
	}
	return env.Enqueue(ctx, KindManifest, c.Key, nil) //nolint:wrapcheck
}

// WorkKinds names the format's one work kind, KindManifest.
func (Handler) WorkKinds() []string { return []string{KindManifest} }

// Owed names the manifest work that a download of a manifest file from a
// local or federated repository must see done first, so a client never
// reads a manifest that lags a completed write; other downloads owe
// nothing.
func (Handler) Owed(c repo.Config, cached bool, path string) []format.WorkRef {
	if derives(c, cached) && isManifest(path) {
		return []format.WorkRef{{Kind: KindManifest, Key: c.Key}}
	}
	return nil
}

// Resolve leaves every path as it is. When a download asks for a manifest
// file of a local or federated repository that holds neither the file nor
// queued manifest work, it queues the work, which Owed then makes the
// download wait for. A store or work queue fault fails the download.
func (Handler) Resolve(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	d format.Download,
) (string, error) {
	if !derives(c, d.Cached) || !isManifest(d.Path) {
		return d.Path, nil
	}
	_, err := env.Store.Items().Get(ctx, c.Key, false, d.Path)
	if err == nil {
		return d.Path, nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return "", fmt.Errorf("luarocks: manifest: %w", err)
	}
	_, err = env.WorkQueue.Get(ctx, KindManifest, c.Key)
	if err == nil {
		return d.Path, nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return "", fmt.Errorf("luarocks: manifest work: %w", err)
	}
	return d.Path, queue(ctx, env, c, false)
}

// Classify classes the manifest files as derived, so replication and
// federation skip them and each side builds its own, and every other path
// as unique content.
func (Handler) Classify(_ repo.Config, path string) format.Class {
	if isManifest(path) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

// Reindex rebuilds the package index in one transaction from every stored
// rockspec and rock at a valid root file name in the tree the index reads:
// it checks each file's content again, sets its properties, groups the
// files by package and version, reconciles the index and queues the
// manifest work. A file whose content no longer passes fails the reindex.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	err := env.Store.Tx(ctx, func(ctx context.Context) error {
		hits, err := files(ctx, env.Store, c)
		if err != nil {
			return err
		}
		by := map[string]map[string][]model.Item{}
		for _, it := range hits {
			id, ok := Parse(it.FullPath())
			if !ok {
				continue
			}
			if err := validateStored(ctx, env.Blobs, it.Actual.SHA256, id); err != nil {
				return err
			}
			if err := setProperties(ctx, env.Store, it, id); err != nil {
				return err
			}
			if by[id.Name] == nil {
				by[id.Name] = map[string][]model.Item{}
			}
			by[id.Name][id.Version] = append(by[id.Name][id.Version], it)
		}
		var wanted []pkgindex.Indexed
		for name, versions := range by {
			p := pkgindex.Indexed{Name: name}
			for version, its := range versions {
				v := indexedVersion(version, its)
				p.Versions = append(p.Versions, v)
				if p.Created.IsZero() || v.Created.Before(p.Created) {
					p.Created = v.Created
				}
			}
			slices.SortFunc(
				p.Versions,
				func(a, b pkgindex.IndexedVersion) int { return strings.Compare(a.Version, b.Version) },
			)
			wanted = append(wanted, p)
		}
		slices.SortFunc(
			wanted,
			func(a, b pkgindex.Indexed) int { return strings.Compare(a.Name, b.Name) },
		)
		if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
			return fmt.Errorf("luarocks: reconcile packages: %w", err)
		}
		return queue(ctx, env, c, pkgindex.Stored(c).Cached)
	})
	if err != nil {
		return fmt.Errorf("luarocks: reindex: %w", err)
	}
	return nil
}

func files(ctx context.Context, st format.Store, c repo.Config) ([]model.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("luarocks: files of %s: %w", c.Key, err)
	}
	out := make([]model.Item, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Item)
	}
	return out, nil
}
