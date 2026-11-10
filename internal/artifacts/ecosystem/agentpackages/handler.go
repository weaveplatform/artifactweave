package agentpackages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Serves reports whether t is the Agent Packages package type.
func Serves(t repo.PackageType) bool { return t == repo.AgentPackages }

type (
	// Handler is the Agent Packages format's hooks: it admits an archive
	// only at its canonical path and when its descriptor agrees with that
	// path, keeps published versions immutable, and keeps the package index
	// and the agentpackages.* properties in step with the stored archives.
	// It embeds the generic hooks, so every path is classed as generic
	// content is.
	Handler    struct{ generic.Handler }
	inspection struct {
		sha  string
		meta Metadata
		err  error
	}
)

// Inspect reads the stored archive of every write but a checksum deploy
// before the write's transaction, and keeps what it read, or why the archive
// is invalid, for Admit and Deployed, so an archive is read once per write.
// A write without a SHA-256 inspects nothing; an archive that cannot be
// opened fails the write.
func (Handler) Inspect(
	ctx context.Context, env format.Env, _ repo.Config, w format.Write,
) (any, error) {
	if w.Checksums.SHA256 == "" || w.Op == format.OpChecksum {
		return nil, nil //nolint:nilnil // no content to inspect
	}
	rc, info, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, fmt.Errorf("agentpackages: open archive: %w", err)
	}
	defer rc.Close()
	meta, err := ReadArchive(rc, info.Size)
	return &inspection{sha: w.Checksums.SHA256, meta: meta, err: err}, nil
}

// Admit refuses a write, other than a checksum deploy or a remote's cached
// content, that is not at a canonical archive path, whose archive Inspect
// did not read or found invalid, or whose apm.yml names another version or
// a name other than the package's own or owner/name (409). It then applies
// the generic blackout and include and exclude checks, keeping their
// refusals as they are.
func (Handler) Admit(
	_ context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if w.Op != format.OpChecksum && !w.Cached {
		owner, name, version, ok := ParsePath(w.Path)
		if !ok {
			return w, format.Rejectf(http.StatusConflict, "invalid Agent Package ZIP path")
		}
		in, good := env.Inspection.(*inspection)
		if !good || in.err != nil || in.meta.Version != "" && in.meta.Version != version ||
			in.meta.Name != "" && in.meta.Name != name && in.meta.Name != owner+"/"+name {
			return w, format.Rejectf(
				http.StatusConflict,
				"invalid Agent Package archive or manifest",
			)
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

// GuardWrite keeps version ZIPs immutable through both APM and ordinary item
// routes. Restore bypasses the guard by design; it reindexes the saved bytes.
func (Handler) GuardWrite(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) error {
	if w.Op == format.OpChecksum || w.Cached {
		return nil
	}
	_, err := env.Store.Items().Get(ctx, c.Key, false, w.Path)
	switch {
	case err == nil:
		return format.Rejectf(http.StatusConflict, "Agent Package version already published")
	case errors.Is(err, kernel.ErrNotFound):
		return nil
	default:
		return fmt.Errorf("agentpackages: check existing version: %w", err)
	}
}

// Deployed indexes a stored archive at a canonical path of the repository's
// own tree; folders, cached items and other files change nothing. An
// archive whose descriptor no longer agrees with its path fails with
// ErrArchive. Otherwise it sets the agentpackages.* properties, dropping
// agentpackages.description when the descriptor has none, creates the
// package owner/name when it is new, and links the version to the archive,
// with its Metadata as the version's metadata.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || it.Cached {
		return nil
	}
	owner, name, version, ok := ParsePath(it.FullPath())
	if !ok {
		return nil
	}
	meta, err := metadataOf(ctx, env, it)
	if err != nil {
		return err
	}
	if meta.Version != "" && meta.Version != version || meta.Name != "" &&
		meta.Name != name && meta.Name != owner+"/"+name {
		return fmt.Errorf("agentpackages: manifest identity of %s: %w", it.FullPath(), ErrArchive)
	}
	meta.Name, meta.Version, meta.Path = owner+"/"+name, version, it.FullPath()
	meta.SHA256, meta.Size = it.Actual.SHA256, it.Size
	if err := setProperties(ctx, env.Store, it, meta); err != nil {
		return err
	}
	p, err := env.Store.Packages().Ensure(ctx, model.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: meta.Name, Created: it.Created,
	})
	if err != nil {
		return fmt.Errorf("agentpackages: package %s: %w", meta.Name, err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("agentpackages: metadata: %w", err)
	}
	_, err = pkgindex.Sync(ctx, env.Store, p, pkgindex.IndexedVersion{
		Version: version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
	})
	return err //nolint:wrapcheck // pkgindex names the version
}

// Deleting does nothing: the item service drops a removed archive's
// version link with it.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted does nothing: a version is one archive, so the item service's
// pruning of versions left without files is all a delete needs.
func (Handler) Deleted(context.Context, format.Env, repo.Config, []model.Item) error { return nil }

func metadataOf(ctx context.Context, env format.Env, it model.Item) (Metadata, error) {
	if in, ok := env.Inspection.(*inspection); ok && in.sha == it.Actual.SHA256 {
		return in.meta, in.err
	}
	rc, info, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return Metadata{}, fmt.Errorf("agentpackages: open stored archive: %w", err)
	}
	defer rc.Close()
	return ReadArchive(rc, info.Size)
}

func setProperties(ctx context.Context, st format.Store, it model.Item, meta Metadata) error {
	current, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("agentpackages: properties of %s: %w", it.FullPath(), err)
	}
	wanted := model.Properties{
		"agentpackages.name": {meta.Name}, "agentpackages.version": {meta.Version},
		"agentpackages.sha256": {meta.SHA256},
	}
	if meta.Description != "" {
		wanted["agentpackages.description"] = []string{meta.Description}
	}
	var remove []string
	set := model.Properties{}
	for _, key := range []string{"agentpackages.name", "agentpackages.version", "agentpackages.sha256", "agentpackages.description"} {
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
		return fmt.Errorf("agentpackages: patch properties of %s: %w", it.FullPath(), err)
	}
	return nil
}

// Reindex rebuilds repository c's package index from the archives at
// canonical paths of its own tree, setting each archive's properties as
// Deployed does, and fails with ErrArchive on the first archive that cannot
// be read or whose descriptor disagrees with its path.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	var all []pkgindex.Indexed
	byName := map[string]int{}
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		items, next, err := env.Store.Items().Descendants(ctx, c.Key, false, "", page)
		if err != nil {
			return fmt.Errorf("agentpackages: list archives: %w", err)
		}
		for _, it := range items {
			owner, name, version, ok := ParsePath(it.FullPath())
			if !ok || it.Folder {
				continue
			}
			meta, err := metadataOf(ctx, env, it)
			if err != nil || meta.Version != "" && meta.Version != version ||
				meta.Name != "" && meta.Name != name && meta.Name != owner+"/"+name {
				return fmt.Errorf("agentpackages: inspect %s: %w", it.FullPath(), ErrArchive)
			}
			meta.Name, meta.Version, meta.Path = owner+"/"+name, version, it.FullPath()
			meta.SHA256, meta.Size = it.Actual.SHA256, it.Size
			if err := setProperties(ctx, env.Store, it, meta); err != nil {
				return err
			}
			data, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("agentpackages: metadata: %w", err)
			}
			i, ok := byName[meta.Name]
			if !ok {
				i = len(all)
				byName[meta.Name] = i
				all = append(all, pkgindex.Indexed{Name: meta.Name, Created: it.Created})
			}
			all[i].Versions = append(all[i].Versions, pkgindex.IndexedVersion{
				Version: version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
			})
		}
		if next == "" {
			break
		}
		page.After = next
	}
	err := pkgindex.Reconcile(
		ctx,
		env.Store,
		c,
		all,
	)
	return err //nolint:wrapcheck // pkgindex names the failure
}

var (
	_ format.Handler    = Handler{}
	_ format.Inspector  = Handler{}
	_ format.Admitter   = Handler{}
	_ format.WriteGuard = Handler{}
	_ format.Reindexer  = Handler{}
)
