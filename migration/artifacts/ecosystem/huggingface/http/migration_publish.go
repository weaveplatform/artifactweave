package huggingfacehttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	format "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/huggingface"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) migrationDestination(
	r *http.Request,
	files []migrationFile,
	status migrationStatus,
) (bool, error) {
	items, err := h.migrationItems(r, status.Destination, types.ActionRead|types.ActionWrite)
	if err != nil {
		return false, err
	}
	existing := make(map[string]types.Item)
	for _, it := range items {
		if !it.Folder {
			existing[it.FullPath()] = it
		}
	}
	if len(existing) == 0 {
		return false, nil
	}
	if len(existing) != len(files) {
		return false, migrationConflict()
	}
	for _, file := range files {
		it, ok := existing[file.path]
		original := file.item.Original
		if original.SHA256 == "" {
			// Normal item publication records the computed SHA256 when the
			// imported source did not declare one. Explicit values are retained.
			original.SHA256 = file.item.Actual.SHA256
		}
		if !ok || it.Actual != file.item.Actual || it.Original != original ||
			it.Size != file.item.Size {
			return false, migrationConflict()
		}
		props, err := h.k.DB().Items().Properties(r.Context(), it.ID)
		if err != nil {
			return false, fmt.Errorf("huggingface: read migrated properties: %w", err)
		}
		want := file.props.Clone()
		want.Set(migrationSourceProperty, status.Source)
		if !props.Equal(want) {
			return false, migrationConflict()
		}
	}
	return true, nil
}

func migrationConflict() error {
	return apierror.New(
		http.StatusConflict,
		"destination exists without an identical complete migration; nothing was changed",
	)
}

func (h handler) migrationRefAvailable(r *http.Request, path, revision string) error {
	if path == "" {
		return nil
	}
	current, err := h.commitParent(r, path)
	if err != nil {
		return err
	}
	if current != "" && current != revision {
		return apierror.New(http.StatusConflict, "destination ref already names another revision")
	}
	return nil
}

func (h handler) publishMigration(
	r *http.Request,
	id format.Identity,
	files []migrationFile,
	status migrationStatus,
	ref string,
) error {
	c := pkgroute.RequestFrom(r).Repo
	paths := make([]string, 0, len(files))
	version := pkgindex.Version{
		RepoKey: c.Key, PackageType: c.PackageType,
		Package: id.Kind + "/" + id.RepoID(), Version: status.Revision,
	}
	var first types.Item
	for _, file := range files {
		info := blob.Info{Size: file.item.Size, Digests: blob.Digests{
			SHA256: file.item.Actual.SHA256, SHA1: file.item.Actual.SHA1,
			SHA512: file.item.Actual.SHA512, MD5: file.item.Actual.MD5,
		}}
		props := file.props.Clone()
		props.Set(migrationSourceProperty, status.Source)
		it, err := h.k.StoreNew(r, file.path, info, props, file.item.Original, migrationConflict)
		if err != nil {
			return fmt.Errorf("huggingface: publish migrated file: %w", err)
		}
		if err = pkgindex.Link(
			r.Context(),
			h.k.DB(),
			version,
			it.ID,
			file.item.Created,
		); err != nil {
			return fmt.Errorf("huggingface: index migrated file: %w", err)
		}
		if first.ID == 0 {
			first = it
		}
		paths = append(paths, it.FullPath())
	}
	pv, ok, err := pkgindex.VersionOf(r.Context(), h.k.DB(), first.ID)
	if err != nil {
		return fmt.Errorf("huggingface: find migrated version: %w", err)
	}
	if !ok {
		return fmt.Errorf("huggingface: migrated version: %w", store.ErrNotFound)
	}
	slices.Sort(paths)
	pv.Metadata, _ = json.Marshal(map[string]any{"revision": status.Revision, "files": paths})
	if _, err = h.k.DB().Packages().PutVersion(r.Context(), pv); err != nil {
		return fmt.Errorf("huggingface: publish migrated version: %w", err)
	}
	if ref != "" {
		_, err = h.k.StoreOnce(
			r,
			ref,
			strings.NewReader(status.Revision),
			nil,
			types.Checksums{},
			func(types.Item) error {
				return h.migrationRefAvailable(r, ref, status.Revision)
			},
		)
		if err != nil {
			return fmt.Errorf("huggingface: publish migrated ref: %w", err)
		}
	}
	if err := r.Context().Err(); err != nil {
		return fmt.Errorf("huggingface: finish migration: %w", err)
	}
	complete, err := h.migrationDestination(r, files, status)
	if err != nil {
		return err
	}
	if !complete {
		return migrationConflict()
	}
	if err = h.migrationPolicy(r, id, files, status, ref); err != nil {
		return err
	}
	return nil
}
