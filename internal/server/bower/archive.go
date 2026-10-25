package bower

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

	bowerfmt "github.com/weaveplatform/artifactweave/internal/format/bower"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	maxArchiveBytes = 128 << 20
	maxArchiveFiles = 4096
)

func (h handler) localBinary(w http.ResponseWriter, r *http.Request, name, version string) {
	folder := name + "/" + version
	var items []types.Item
	var total int64
	hasManifest := false
	for page := (store.Page{Limit: store.MaxLimit}); ; {
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
				!h.k.Can(r, it.FullPath(), types.ActionRead) {
				h.k.Error(
					w,
					r,
					apierror.New(http.StatusForbidden, "Bower package contains unreadable files"),
				)
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
	http.ServeContent(w, r, name+"-"+version+".tar.gz", time.Time{}, bytes.NewReader(out.Bytes()))
}
