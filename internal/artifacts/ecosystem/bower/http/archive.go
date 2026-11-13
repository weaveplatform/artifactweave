package bowerhttp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	bowerfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bower"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const (
	maxArchiveBytes = 128 << 20
	maxArchiveFiles = 4096
)

// localBinary answers the archive of a version's stored files. It reads
// their blobs itself, so it applies the download policy the kit's Item
// would: to the version's manifest first, so that a blacked-out
// repository's answer names the same file whichever the listing reaches
// first, then to each file the archive holds.
func (h handler) localBinary(w http.ResponseWriter, r *http.Request, name, version string) {
	if err := h.k.CheckDownload(r, bowerfmt.ManifestPath(name, version)); err != nil {
		h.k.Error(w, r, err)
		return
	}
	folder := name + "/" + version
	var items []model.Item
	var total int64
	hasManifest := false
	for page := (kernel.Page{Limit: kernel.MaxLimit}); ; {
		batch, next, err := h.k.DB().
			Items().
			Descendants(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, folder, page)
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("bower list package files: %w", err))
			return
		}
		for _, it := range batch {
			if it.Folder {
				continue
			}
			_, _, _, ok := bowerfmt.ParsePath(it.FullPath())
			if !ok || !strings.HasPrefix(it.FullPath(), folder+"/") ||
				!h.k.Can(r, it.FullPath(), api.ActionRead) {
				h.k.Error(
					w,
					r,
					apierror.New(http.StatusForbidden, "Bower package contains unreadable files"),
				)
				return
			}
			if err := h.k.CheckDownload(r, it.FullPath()); err != nil {
				h.k.Error(w, r, err)
				return
			}
			items = append(items, it)
			total += it.Size
			if len(items) > maxArchiveFiles || total > maxArchiveBytes {
				h.k.Error(
					w,
					r,
					apierror.New(
						http.StatusRequestEntityTooLarge,
						"Bower package archive exceeds limit",
					),
				)
				return
			}
			hasManifest = hasManifest || it.FullPath() == bowerfmt.ManifestPath(name, version)
		}
		if next == "" {
			break
		}
		page.After = next
	}
	if !hasManifest {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Bower version not found"))
		return
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, it := range items {
		if err := r.Context().Err(); err != nil {
			h.k.Error(w, r, err)
			return
		}
		rel := strings.TrimPrefix(it.FullPath(), folder+"/")
		header := &tar.Header{
			Name: name + "-" + version + "/" + rel,
			Mode: 0o644, Size: it.Size, ModTime: it.Modified.UTC(),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			h.k.Error(w, r, fmt.Errorf("bower archive header: %w", err))
			return
		}
		body, _, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("bower archive blob: %w", err))
			return
		}
		n, copyErr := io.CopyN(tw, body, it.Size)
		closeErr := body.Close()
		if copyErr != nil || closeErr != nil || n != it.Size {
			h.k.Error(w, r, fmt.Errorf("bower archive content: %w", errors.Join(copyErr, closeErr)))
			return
		}
		if out.Len() > maxArchiveBytes {
			h.k.Error(
				w,
				r,
				apierror.New(
					http.StatusRequestEntityTooLarge,
					"Bower package archive exceeds limit",
				),
			)
			return
		}
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		h.k.Error(w, r, fmt.Errorf("bower archive finish: %w", err))
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "private, no-store")
	itemkit.ServeContent(
		w,
		r,
		name+"-"+version+".tar.gz",
		time.Time{},
		bytes.NewReader(out.Bytes()),
	)
}
