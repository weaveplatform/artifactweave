// Package luarocks indexes root-level rockspecs and rocks and derives the
// LuaRocks manifest consumed by the native client.
package luarocks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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

const (
	KindManifest = "luarocks.manifest"
	ManifestPath = "manifest"
	PropName     = "luarocks.name" // [I] exact 7.161.32 property spelling: #216
	PropVersion  = "luarocks.version"
	PropRevision = "luarocks.revision"
	PropArch     = "luarocks.arch"
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

func Serves(t repo.PackageType) bool { return t == repo.LuaRocks }

type Handler struct{ generic.Handler }

type inspection struct {
	sha string
	id  Identity
}

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

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
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

func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
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

func (Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
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

func (Handler) WorkKinds() []string { return []string{KindManifest} }
func (Handler) Owed(c repo.Config, cached bool, path string) []format.WorkRef {
	if derives(c, cached) && isManifest(path) {
		return []format.WorkRef{{Kind: KindManifest, Key: c.Key}}
	}
	return nil
}

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
	if !errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("luarocks: manifest: %w", err)
	}
	_, err = env.Store.WorkQueue().Get(ctx, KindManifest, c.Key)
	if err == nil {
		return d.Path, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("luarocks: manifest work: %w", err)
	}
	return d.Path, queue(ctx, env, c, false)
}

func (Handler) Classify(_ repo.Config, path string) format.Class {
	if isManifest(path) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	err := env.Store.Tx(ctx, func(ctx context.Context) error {
		hits, err := files(ctx, env.Store, c)
		if err != nil {
			return err
		}
		by := map[string]map[string][]types.Item{}
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
				by[id.Name] = map[string][]types.Item{}
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

func files(ctx context.Context, st store.Store, c repo.Config) ([]types.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("luarocks: files of %s: %w", c.Key, err)
	}
	out := make([]types.Item, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Item)
	}
	return out, nil
}
