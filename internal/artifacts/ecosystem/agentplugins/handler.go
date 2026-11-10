package agentplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Serves reports whether t is the Agent Plugins package type.
func Serves(t repo.PackageType) bool { return t == repo.AgentPlugins }

type (
	// Handler is the Agent Plugins format's hooks: it admits a plugin bundle
	// at its canonical path only when it carries a harness manifest, keeps
	// the package index and the agentplugins.* properties in step with the
	// stored bundles, and keeps uploads off the marketplace file names. It
	// embeds the generic hooks, so every path is classed as generic content
	// is.
	Handler    struct{ generic.Handler }
	inspection struct {
		sha  string
		meta Metadata
		err  error
	}
)

// Inspect reads the stored bundle of a write to a .zip path before the
// write's transaction, and keeps what it read, or why the bundle is
// invalid, for Admit and Deployed, so a bundle is read once per write. A
// write without a SHA-256 and any other file inspect nothing; a bundle that
// cannot be opened fails the write.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
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

// Admit refuses a write, other than a checksum deploy or a remote's cached
// content, to a marketplace file name (409), since those files are rendered
// for each reader and never stored, and a .zip that is not at a canonical
// bundle path or whose bundle Inspect found invalid or without a harness
// manifest (409). It then applies the generic blackout and include and
// exclude checks, keeping their refusals as they are.
func (Handler) Admit(
	_ context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if w.Op != format.OpChecksum && !w.Cached && IsReservedMarketplace(w.Path) {
		return w, format.Rejectf(http.StatusConflict, "Agent Plugin marketplace path is reserved")
	}
	if w.Op != format.OpChecksum && !w.Cached && strings.HasSuffix(w.Path, ".zip") {
		if _, _, ok := ParsePath(w.Path); !ok {
			return w, format.Rejectf(http.StatusConflict, "invalid Agent Plugin bundle path")
		}
		in, ok := env.Inspection.(*inspection)
		if !ok || in.err != nil || len(in.meta.Harnesses) == 0 {
			return w, format.Rejectf(http.StatusConflict, "invalid Agent Plugin bundle")
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

// Deployed indexes a stored bundle at a canonical path of the repository's
// own tree; folders, cached items and other files change nothing. It sets
// the agentplugins.* properties, dropping agentplugins.sha1 when the item
// has no SHA-1, creates the plugin's package when it is new, and links the
// version to the bundle, with its Metadata, named and versioned by the path,
// as the version's metadata. A bundle without a harness manifest fails with
// ErrArchive: the 7.161.32 target indexes harness manifests only, so a
// root-only Agent Plugins Standard bundle is not indexed.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
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
		return fmt.Errorf(
			"agentplugins: root-only bundle unsupported on 7.161.32 at %s: %w",
			it.FullPath(), ErrArchive,
		)
	}
	meta.Name, meta.Version, meta.Path = name, version, it.FullPath()
	meta.SHA256, meta.SHA1 = it.Actual.SHA256, it.Actual.SHA1
	if err := setProperties(ctx, env.Store, it, meta); err != nil {
		return err
	}
	return index(ctx, env.Store, c, it, meta)
}

// Deleting does nothing: the item service drops a removed bundle's version
// link with it.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted does nothing: a version is one bundle, so the item service's
// pruning of versions left without files is all a delete needs.
func (Handler) Deleted(context.Context, format.Env, repo.Config, []model.Item) error { return nil }

func metadataOf(ctx context.Context, env format.Env, it model.Item) (Metadata, error) {
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

func setProperties(ctx context.Context, st format.Store, it model.Item, meta Metadata) error {
	current, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("agentplugins: properties of %s: %w", it.FullPath(), err)
	}
	wanted := model.Properties{
		"agentplugins.name": {meta.Name}, "agentplugins.version": {meta.Version},
		"agentplugins.type": meta.Harnesses, "agentplugins.sha256": {meta.SHA256},
	}
	if meta.SHA1 != "" {
		wanted["agentplugins.sha1"] = []string{meta.SHA1}
	}
	set := model.Properties{}
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

func index(
	ctx context.Context,
	st format.Store,
	c repo.Config,
	it model.Item,
	meta Metadata,
) error {
	p, err := st.Packages().
		Ensure(ctx, model.Package{RepoKey: c.Key, Type: c.PackageType, Name: meta.Name, Created: it.Created})
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

// Reindex rebuilds repository c's package index from the bundles at
// canonical paths of its own tree, setting each bundle's properties as
// Deployed does, and fails with ErrArchive on the first bundle that cannot
// be read or has no harness manifest.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	var all []pkgindex.Indexed
	byName := map[string]int{}
	page := kernel.Page{Limit: kernel.MaxLimit}
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
			all[i].Versions = append(
				all[i].Versions,
				pkgindex.IndexedVersion{
					Version:  version,
					Created:  it.Created,
					Metadata: data,
					Files:    []int64{it.ID},
				},
			)
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
