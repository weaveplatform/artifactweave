package ansible

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Serves reports whether t is the Ansible package type.
func Serves(t repo.PackageType) bool { return t == repo.Ansible }

// Handler is the Ansible format's hooks: it admits only a valid collection
// archive at the path its MANIFEST.json identity gives, records a stored
// archive's identity and SHA-256 as ansible.* properties, and indexes it
// as a version of <namespace>.<name>. Admit applies the generic deploy
// policy first.
type Handler struct{ generic.Handler }

type inspection struct {
	sha  string
	meta Metadata
	err  error
}

// Inspect reads and validates the stored archive of a write to a .tar.gz
// path (ReadArchive) before the write's transaction, and returns the result
// for Admit and Deployed, so an archive is read once per write. A checksum
// deploy and any other path inspect nothing. An invalid archive does not
// fail Inspect, because Admit refuses it; one that cannot be opened fails
// the write.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !strings.HasSuffix(w.Path, ".tar.gz") {
		return nil, nil //nolint:nilnil // only release archives carry metadata
	}
	rc, info, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, fmt.Errorf("ansible: open archive: %w", err)
	}
	defer rc.Close()
	meta, err := ReadArchive(rc, info.Size)
	return &inspection{sha: w.Checksums.SHA256, meta: meta, err: err}, nil
}

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a checksum deploy and a write to a remote's cache. Any other write
// must be of a .tar.gz (400) at a collection archive path (400), with
// content Inspect read as a valid archive whose MANIFEST.json names the
// namespace, name and version the path gives (400).
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("ansible: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum || w.Cached {
		return w, nil
	}
	if !strings.HasSuffix(w.Path, ".tar.gz") {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"Ansible repositories accept collection archives",
		)
	}
	ns, name, version, ok := ParsePath(w.Path)
	if !ok {
		return w, format.Rejectf(http.StatusBadRequest, "invalid Ansible collection path")
	}
	in, ok := env.Inspection.(*inspection)
	if !ok || in.sha != w.Checksums.SHA256 || in.err != nil ||
		in.meta.Namespace != ns || in.meta.Name != name || in.meta.Version != version {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"collection archive does not match its path or is invalid",
		)
	}
	return w, nil
}

// Deployed records the ansible.* properties of an archive stored at a
// collection archive path, writing only those that changed, and syncs its
// version in the package index under <namespace>.<name>, with the
// archive's Metadata and the archive as its only file. It reuses Inspect's
// reading when it covers the same content and reads the stored archive
// otherwise; an archive that does not validate fails the write. Folders, a
// remote's cached files and other paths change nothing.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || it.Cached {
		return nil
	}
	if _, _, _, ok := ParsePath(it.FullPath()); !ok {
		return nil
	}
	meta, err := metadataOf(ctx, env, it)
	if err != nil {
		return err
	}
	if err := setProperties(ctx, env.Store, it, meta); err != nil {
		return err
	}
	return index(ctx, env.Store, c, it, meta)
}

// Deleting does nothing: an archive is its version's only file, and the
// item service prunes a version a delete leaves without files.
func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

// Deleted does nothing: the item service has already pruned the versions
// the delete left without files, and no other file belongs to a version.
func (Handler) Deleted(context.Context, format.Env, repo.Config, []types.Item) error { return nil }

func metadataOf(ctx context.Context, env format.Env, it types.Item) (Metadata, error) {
	if in, ok := env.Inspection.(*inspection); ok && in.sha == it.Actual.SHA256 {
		return in.meta, in.err
	}
	rc, info, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return Metadata{}, fmt.Errorf("ansible: open stored archive: %w", err)
	}
	defer rc.Close()
	return ReadArchive(rc, info.Size)
}

var propertyKeys = []string{
	"ansible.namespace", "ansible.name", "ansible.version", "ansible.sha256",
}

func setProperties(ctx context.Context, st store.Store, it types.Item, meta Metadata) error {
	current, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("ansible: item properties: %w", err)
	}
	wanted := types.Properties{
		"ansible.namespace": {meta.Namespace},
		"ansible.name":      {meta.Name},
		"ansible.version":   {meta.Version},
		"ansible.sha256":    {meta.SHA256},
	}
	set := types.Properties{}
	for _, key := range propertyKeys {
		if !slices.Equal(current[key], wanted[key]) {
			set[key] = wanted[key]
		}
	}
	if len(set) == 0 {
		return nil
	}
	if err := st.Items().PatchProperties(ctx, it.ID, set, nil, it.Updated); err != nil {
		return fmt.Errorf("ansible: update properties: %w", err)
	}
	return nil
}

func index(ctx context.Context, st store.Store, c repo.Config, it types.Item, meta Metadata) error {
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Type: c.PackageType,
		Name: meta.Namespace + "." + meta.Name, Created: it.Created,
	})
	if err != nil {
		return fmt.Errorf("ansible: package: %w", err)
	}
	data, _ := json.Marshal(meta) // Metadata contains only JSON-safe strings and maps.
	_, err = pkgindex.Sync(ctx, st, p, pkgindex.IndexedVersion{
		Version: meta.Version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
	})
	return err //nolint:wrapcheck // pkgindex names the failed version
}

// Reindex rebuilds the package index from every archive stored at a
// collection archive path: it reads each archive again, records its
// properties unless the reindex operation keeps item properties
// (SkipProperties), groups the versions by <namespace>.<name> and
// reconciles the index. An archive that no longer validates, or whose
// identity no longer matches its path, fails the reindex with ErrArchive.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	var all []pkgindex.Indexed
	byName := map[string]int{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		items, next, err := env.Store.Items().Descendants(ctx, c.Key, false, "", page)
		if err != nil {
			return fmt.Errorf("ansible: list archives: %w", err)
		}
		for _, it := range items {
			ns, name, version, ok := ParsePath(it.FullPath())
			if !ok || it.Folder {
				continue
			}
			meta, err := metadataOf(ctx, env, it)
			if err != nil || meta.Namespace != ns || meta.Name != name || meta.Version != version {
				return fmt.Errorf(
					"ansible: invalid stored collection %s: %w",
					it.FullPath(),
					ErrArchive,
				)
			}
			if !env.SkipProperties {
				if err := setProperties(ctx, env.Store, it, meta); err != nil {
					return err
				}
			}
			data, _ := json.Marshal(meta) // Metadata contains only JSON-safe strings and maps.
			fullName := ns + "." + name
			i, found := byName[fullName]
			if !found {
				i = len(all)
				byName[fullName] = i
				all = append(all, pkgindex.Indexed{Name: fullName, Created: it.Created})
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
	if err := pkgindex.Reconcile(ctx, env.Store, c, all); err != nil {
		return fmt.Errorf("ansible: reindex: %w", err)
	}
	return nil
}

var (
	_ format.Handler   = Handler{}
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)
