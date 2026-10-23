package agentplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func Serves(t repo.PackageType) bool { return t == repo.AgentPlugins }

type Handler struct{ generic.Handler }
type inspection struct {
	sha  string
	meta Metadata
	err  error
}

func (Handler) Inspect(ctx context.Context, env format.Env, _ repo.Config, w format.Write) (any, error) {
	if w.Checksums.SHA256 == "" || !strings.HasSuffix(w.Path, ".zip") {
		return nil, nil //nolint:nilnil // no ZIP to inspect
	}
	rc, info, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, fmt.Errorf("agentplugins: open bundle: %w", err)
	}
	defer rc.Close()
	meta, err := ReadArchive(rc, info.Size)
	return &inspection{sha: w.Checksums.SHA256, meta: meta, err: err}, nil
}
func (Handler) Admit(_ context.Context, env format.Env, c repo.Config, w format.Write) (format.Write, error) {
	if w.Op != format.OpChecksum && !w.Cached && strings.HasSuffix(w.Path, ".zip") {
		if _, _, ok := ParsePath(w.Path); !ok {
			return w, format.Reject(http.StatusConflict, "invalid Agent Plugin bundle path")
		}
		in, ok := env.Inspection.(*inspection)
		if !ok || in.err != nil || len(in.meta.Harnesses) == 0 {
			return w, format.Reject(http.StatusConflict, "invalid Agent Plugin bundle")
		}
	}
	if err := generic.CheckBlackedOut(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // preserve policy rejection
	}
	if err := generic.CheckPatterns(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // preserve policy rejection
	}
	return w, nil
}
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || it.Cached || !strings.HasSuffix(it.FullPath(), ".zip") {
		return nil
	}
	name, version, ok := ParsePath(it.FullPath())
	if !ok {
		return nil
	}
	meta, err := metadataOf(ctx, env, it)
	if err != nil {
		return err
	}
	if len(meta.Harnesses) == 0 {
		return fmt.Errorf("agentplugins: root-only bundle unsupported on 7.161.32: %s", it.FullPath())
	}
	meta.Name, meta.Version, meta.Path = name, version, it.FullPath()
	meta.SHA256, meta.SHA1 = it.Actual.SHA256, it.Actual.SHA1
	if err := setProperties(ctx, env.Store, it, meta); err != nil {
		return err
	}
	return index(ctx, env.Store, c, it, meta)
}
func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error  { return nil }
func (Handler) Deleted(context.Context, format.Env, repo.Config, []types.Item) error { return nil }

func metadataOf(ctx context.Context, env format.Env, it types.Item) (Metadata, error) {
	if in, ok := env.Inspection.(*inspection); ok && in.sha == it.Actual.SHA256 {
		return in.meta, in.err
	}
	rc, info, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return Metadata{}, fmt.Errorf("agentplugins: open stored bundle: %w", err)
	}
	defer rc.Close()
	return ReadArchive(rc, info.Size)
}
func setProperties(ctx context.Context, st store.Store, it types.Item, meta Metadata) error {
	current, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("agentplugins: properties of %s: %w", it.FullPath(), err)
	}
	wanted := types.Properties{
		"agentplugins.name": {meta.Name}, "agentplugins.version": {meta.Version},
		"agentplugins.type": meta.Harnesses, "agentplugins.sha256": {meta.SHA256},
	}
	if meta.SHA1 != "" {
		wanted["agentplugins.sha1"] = []string{meta.SHA1}
	}
	set := types.Properties{}
	var remove []string
	for _, key := range []string{"agentplugins.name", "agentplugins.version", "agentplugins.type", "agentplugins.sha256", "agentplugins.sha1"} {
		want, yes := wanted[key]
		have, had := current[key]
		switch {
		case yes && !slices.Equal(have, want):
			set[key] = want
		case !yes && had:
			remove = append(remove, key)
		}
	}
	if len(set) == 0 && len(remove) == 0 {
		return nil
	}
	if err := st.Items().PatchProperties(ctx, it.ID, set, remove, it.Updated); err != nil {
		return fmt.Errorf("agentplugins: patch properties of %s: %w", it.FullPath(), err)
	}
	return nil
}
func index(ctx context.Context, st store.Store, c repo.Config, it types.Item, meta Metadata) error {
	p, err := st.Packages().Ensure(ctx, types.Package{RepoKey: c.Key, Type: c.PackageType, Name: meta.Name, Created: it.Created})
	if err != nil {
		return fmt.Errorf("agentplugins: package %s: %w", meta.Name, err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("agentplugins: metadata: %w", err)
	}
	_, err = pkgindex.Sync(ctx, st, p, pkgindex.IndexedVersion{
		Version: meta.Version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
	})
	return err //nolint:wrapcheck // pkgindex names the version
}
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	var all []pkgindex.Indexed
	byName := map[string]int{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		items, next, err := env.Store.Items().Descendants(ctx, c.Key, false, "", page)
		if err != nil {
			return fmt.Errorf("agentplugins: list bundles: %w", err)
		}
		for _, it := range items {
			name, version, ok := ParsePath(it.FullPath())
			if !ok || it.Folder {
				continue
			}
			meta, err := metadataOf(ctx, env, it)
			if err != nil || len(meta.Harnesses) == 0 {
				return fmt.Errorf("agentplugins: inspect %s: %w", it.FullPath(), ErrArchive)
			}
			meta.Name, meta.Version, meta.Path = name, version, it.FullPath()
			meta.SHA256, meta.SHA1 = it.Actual.SHA256, it.Actual.SHA1
			if err := setProperties(ctx, env.Store, it, meta); err != nil {
				return err
			}
			data, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("agentplugins: metadata: %w", err)
			}
			i, ok := byName[name]
			if !ok {
				i = len(all)
				byName[name] = i
				all = append(all, pkgindex.Indexed{Name: name, Created: it.Created})
			}
			all[i].Versions = append(all[i].Versions, pkgindex.IndexedVersion{Version: version, Created: it.Created, Metadata: data, Files: []int64{it.ID}})
		}
		if next == "" {
			break
		}
		page.After = next
	}
	if err := pkgindex.Reconcile(ctx, env.Store, c, all); err != nil {
		return err //nolint:wrapcheck // pkgindex wraps error
	}
	return nil
}

var (
	_ format.Handler   = Handler{}
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)
