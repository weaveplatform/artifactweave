package conda

import (
	"bytes"
	"context"
	"errors"
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

const (
	KindIndex   = "conda.repodata"
	PropName    = "conda.name"
	PropVersion = "conda.version"
	PropBuild   = "conda.build"
	PropSubdir  = "conda.subdir"
	PropChannel = "conda.channel"
)

func Serves(t repo.PackageType) bool { return t == repo.Conda }

type Handler struct {
	generic.Handler
	Settings Settings
}

type inspection struct {
	sha, path string
	record    Record
}

func derives(c repo.Config) bool { return c.Class == repo.Local || c.Class == repo.Federated }

func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum {
		return inspection{}, nil
	}
	id, ok := ParsePackagePath(w.Path)
	if !ok {
		return inspection{}, nil
	}
	rec, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, id)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, record: rec}, nil
}

func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("conda: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	if !w.Cached && derives(c) && IsMetadata(w.Path) {
		return w, format.Rejectf(
			http.StatusConflict,
			"Conda metadata is generated from package archives",
		)
	}
	if !strings.HasSuffix(w.Path, ".conda") && !strings.HasSuffix(w.Path, ".tar.bz2") {
		return w, nil
	}
	id, ok := ParsePackagePath(w.Path)
	if !ok {
		return w, format.Rejectf(http.StatusBadRequest, "Invalid Conda package path")
	}
	if in, ok := env.Inspection.(inspection); !ok || in.sha != w.Checksums.SHA256 ||
		in.path != w.Path {
		if _, err := readStored(ctx, env.Blobs, w.Checksums.SHA256, id); err != nil {
			return w, err
		}
	}
	return w, nil
}

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder {
		return nil
	}
	id, ok := ParsePackagePath(it.FullPath())
	if !ok {
		return nil
	}
	rec, err := packageFor(ctx, env, it, id)
	if err != nil {
		return err
	}
	if err := setProperties(ctx, env.Store, it, id); err != nil {
		return err
	}
	if err := syncVersion(ctx, env, c, rec.Name, rec.Version); err != nil {
		return err
	}
	return queue(ctx, env, c, it.Cached)
}

func (Handler) Deleting(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder {
		return nil
	}
	if _, ok := ParsePackagePath(it.FullPath()); ok {
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
	seen := map[string]bool{}
	for _, it := range removed {
		id, ok := ParsePackagePath(it.FullPath())
		if !ok {
			continue
		}
		key := id.Name + "\x00" + id.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := syncVersion(ctx, env, c, id.Name, id.Version); err != nil {
			return err
		}
	}
	return nil
}

func queue(ctx context.Context, env format.Env, c repo.Config, cached bool) error {
	if cached || !derives(c) {
		return nil
	}
	return env.Enqueue(ctx, KindIndex, c.Key, nil) //nolint:wrapcheck
}

func (Handler) WorkKinds() []string { return []string{KindIndex} }

func (Handler) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if !cached && derives(c) && IsMetadata(p) {
		return []format.WorkRef{{Kind: KindIndex, Key: c.Key}}
	}
	return nil
}

func (Handler) Classify(_ repo.Config, p string) format.Class {
	if IsMetadata(p) {
		return format.ClassDerived
	}
	return format.ClassUnique
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	by := map[string]map[string][]types.Item{}
	for _, it := range items {
		id, ok := ParsePackagePath(it.FullPath())
		if !ok {
			continue
		}
		if _, err := packageFor(ctx, env, it, id); err != nil {
			return err
		}
		if !env.SkipProperties {
			if err := setProperties(ctx, env.Store, it, id); err != nil {
				return err
			}
		}
		if by[id.Name] == nil {
			by[id.Name] = map[string][]types.Item{}
		}
		by[id.Name][id.Version] = append(by[id.Name][id.Version], it)
	}
	wanted := make([]pkgindex.Indexed, 0, len(by))
	for name, versions := range by {
		p := pkgindex.Indexed{Name: name}
		for version, its := range versions {
			v := indexedVersion(version, its)
			p.Versions = append(p.Versions, v)
			if p.Created.IsZero() || v.Created.Before(p.Created) {
				p.Created = v.Created
			}
		}
		slices.SortFunc(p.Versions, func(a, b pkgindex.IndexedVersion) int {
			return strings.Compare(a.Version, b.Version)
		})
		wanted = append(wanted, p)
	}
	slices.SortFunc(wanted, func(a, b pkgindex.Indexed) int {
		return strings.Compare(a.Name, b.Name)
	})
	if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
		return fmt.Errorf("conda: reconcile packages: %w", err)
	}
	return queue(ctx, env, c, pkgindex.Stored(c).Cached)
}

func (h Handler) Work(ctx context.Context, env format.Env, work types.WorkItem) error {
	if work.Kind != KindIndex {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, work.Key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("conda: repository: %w", err)
	case !Serves(c.PackageType) || !derives(c):
		return nil
	}
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	bySubdir := map[string]Repodata{}
	old := map[string]bool{}
	for _, it := range items {
		p := it.FullPath()
		if IsMetadata(p) {
			old[p] = true
			continue
		}
		id, ok := ParsePackagePath(p)
		if !ok {
			continue
		}
		rec, err := packageFor(ctx, env, it, id)
		if err != nil {
			return err
		}
		rec.Size, rec.SHA256, rec.MD5 = it.Size, it.Actual.SHA256, it.Actual.MD5
		key := id.Channel + "\x00" + id.Subdir
		index, exists := bySubdir[key]
		if !exists {
			index = EmptyRepodata(id.Subdir)
		}
		if id.Extension == ".conda" {
			index.PackagesConda[id.Filename] = rec
		} else {
			index.Packages[id.Filename] = rec
		}
		bySubdir[key] = index
	}
	for key, index := range bySubdir {
		channel, subdir, _ := strings.Cut(key, "\x00")
		generated, err := EncodeRepodata(index, channel, subdir)
		if err != nil {
			return err
		}
		if h.Settings.ShardsEnabled {
			shards, err := EncodeShards(index, channel, subdir, "./")
			if err != nil {
				return err
			}
			for name, body := range shards {
				generated[name] = body
			}
		}
		for name, body := range generated {
			p := IndexPath(channel, subdir, name)
			if _, err := env.Derive(ctx, format.Derived{
				Repo: c, Path: p, Body: bytes.NewReader(body), By: "_system_",
			}); err != nil {
				return fmt.Errorf("conda: derive %s: %w", p, err)
			}
			delete(old, p)
		}
	}
	for p := range old {
		if err := env.Underive(ctx, c, false, p); err != nil {
			return fmt.Errorf("conda: remove stale %s: %w", p, err)
		}
	}
	return nil
}

func packageFor(
	ctx context.Context,
	env format.Env,
	it types.Item,
	id PackagePath,
) (Record, error) {
	if in, ok := env.Inspection.(inspection); ok && in.sha == it.Actual.SHA256 &&
		in.path == it.FullPath() {
		return in.record, nil
	}
	return readStored(ctx, env.Blobs, it.Actual.SHA256, id)
}

func readStored(
	ctx context.Context,
	blobs format.Blobs,
	sha string,
	id PackagePath,
) (Record, error) {
	if sha == "" {
		return Record{}, format.Rejectf(http.StatusBadRequest, "Conda package content is missing")
	}
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return Record{}, fmt.Errorf("conda: open package: %w", err)
	}
	defer func() { _ = rc.Close() }()
	rec, err := ReadPackage(ctx, rc, info.Size, id)
	if err != nil {
		return Record{}, format.Rejectf(http.StatusBadRequest, "Invalid Conda package: %v", err)
	}
	return rec, nil
}

func setProperties(ctx context.Context, st store.Store, it types.Item, id PackagePath) error {
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("conda: item properties: %w", err)
	}
	want := types.Properties{
		PropName: {id.Name}, PropVersion: {id.Version}, PropBuild: {id.Build},
		PropSubdir: {id.Subdir}, PropChannel: {id.Channel},
	}
	set := types.Properties{}
	for key, values := range want {
		if !slices.Equal(props[key], values) {
			set[key] = values
		}
	}
	if len(set) == 0 {
		return nil
	}
	return st.Items().PatchProperties(ctx, it.ID, set, nil, it.Updated) //nolint:wrapcheck
}

func syncVersion(ctx context.Context, env format.Env, c repo.Config, name, version string) error {
	items, err := files(ctx, env.Store, c)
	if err != nil {
		return err
	}
	var selected []types.Item
	for _, it := range items {
		id, ok := ParsePackagePath(it.FullPath())
		if ok && id.Name == name && id.Version == version {
			selected = append(selected, it)
		}
	}
	if len(selected) == 0 {
		return nil
	}
	v := indexedVersion(version, selected)
	p, err := env.Store.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: name, Created: v.Created,
	})
	if err != nil {
		return fmt.Errorf("conda: package %s: %w", name, err)
	}
	_, err = pkgindex.Sync(ctx, env.Store, p, v)
	return err //nolint:wrapcheck
}

func indexedVersion(version string, items []types.Item) pkgindex.IndexedVersion {
	v := pkgindex.IndexedVersion{Version: version}
	for _, it := range items {
		if v.Created.IsZero() || it.Created.Before(v.Created) {
			v.Created = it.Created
		}
		v.Files = append(v.Files, it.ID)
	}
	slices.Sort(v.Files)
	return v
}

func files(ctx context.Context, st store.Store, c repo.Config) ([]types.Item, error) {
	hits, err := st.Items().Search(ctx, &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{pkgindex.Stored(c)}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
	}}, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("conda: files of %s: %w", c.Key, err)
	}
	out := make([]types.Item, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Item)
	}
	return out, nil
}

var (
	_ format.Handler    = Handler{}
	_ format.Inspector  = Handler{}
	_ format.Admitter   = Handler{}
	_ format.Worker     = Handler{}
	_ format.Classifier = Handler{}
	_ format.Reindexer  = Handler{}
)
