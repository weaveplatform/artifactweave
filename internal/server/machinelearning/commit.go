package machinelearning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func readCommit(w http.ResponseWriter, r *http.Request) (ml.Manifest, types.Properties, error) {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" {
		return ml.Manifest{}, nil, apierror.New(
			http.StatusUnsupportedMediaType,
			"expected multipart model manifest",
		)
	}
	reader := multipart.NewReader(
		http.MaxBytesReader(w, r.Body, ml.MaxManifestSize+(64<<10)),
		params["boundary"],
	)
	seen := map[string]bool{}
	var manifest ml.Manifest
	props := types.Properties{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest, nil, badRequest("invalid or oversized multipart request")
		}
		name := part.FormName()
		if seen[name] || name != "modelManifest" && name != "additionalData" {
			return manifest, nil, badRequest("unexpected multipart field")
		}
		seen[name] = true
		if name == "modelManifest" {
			manifest, err = ml.Decode(part)
			if err != nil {
				return manifest, nil, fmt.Errorf("machinelearning: decode manifest: %w", err)
			}
		} else {
			data, err := io.ReadAll(io.LimitReader(part, 64<<10+1))
			values := map[string]string{}
			if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &values) != nil ||
				len(values) > 256 {
				return manifest, nil, badRequest("invalid model properties")
			}
			for key, value := range values {
				if len(key) > 255 || len(value) > 4096 {
					return manifest, nil, badRequest("model properties exceed limits")
				}
				props[key] = []string{value}
			}
		}
		_ = part.Close()
	}
	if !seen["modelManifest"] {
		return manifest, nil, badRequest("missing model manifest")
	}
	return manifest, props, nil
}

func (h handler) commit(w http.ResponseWriter, r *http.Request) {
	id, err := identity(r, false)
	tx := chi.URLParam(r, "transaction")
	if err != nil || !ml.Transaction(tx) {
		h.fail(w, r, badRequest("invalid model transaction"))
		return
	}
	if !h.k.RequireFolder(w, r, id.Prefix(), types.ActionWrite) {
		return
	}
	m, props, err := readCommit(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if m.ID != "" && m.ID != id.Name || m.Version != "" && m.Version != id.Version {
		h.fail(w, r, badRequest("manifest identity differs from the transaction"))
		return
	}
	m.ID, m.Version = id.Name, id.Version
	for i := range m.Artifacts {
		m.Artifacts[i].DownloadURL = ""
	}
	for i := range m.Dependencies {
		m.Dependencies[i].DownloadURL = ""
	}
	if m.Code != nil {
		m.Code.DownloadURL = ""
	}
	data, err := json.Marshal(m)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	request := r.WithContext(ml.Publication(r.Context()))
	info, err := h.k.Upload(request, bytes.NewReader(data))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	err = h.k.DB().Tx(r.Context(), func(ctx context.Context) error {
		return h.publish(r.WithContext(ml.Publication(ctx)), id, tx, m, props, info)
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id.Name, "version": id.Version})
}

func (h handler) publish(
	r *http.Request,
	id ml.Identity,
	tx string,
	m ml.Manifest,
	props types.Properties,
	info blob.Info,
) error {
	ctx := r.Context()
	c := pkgroute.RequestFrom(r).Repo
	p, _ := auth.FromContext(ctx)
	by := p.Name
	if p.Anonymous {
		by = ""
	}
	if err := ml.CheckTransaction(ctx, h.env(), c, id, tx, by); err != nil {
		return fmt.Errorf("machinelearning: publish: %w", err)
	}
	for _, file := range m.Files() {
		if !h.k.Can(r, id.Prefix()+"/"+file.Path, types.ActionWrite) {
			return apierror.New(http.StatusForbidden, "model artifact deployment is not authorized")
		}
	}
	if !h.k.Can(r, id.Manifest(), types.ActionWrite) {
		return apierror.New(http.StatusForbidden, "model publication is not authorized")
	}
	existing, err := h.k.DB().Items().Get(ctx, c.Key, false, id.Manifest())
	if err == nil {
		oldProps, err := h.k.DB().Items().Properties(ctx, existing.ID)
		if err != nil {
			return fmt.Errorf("machinelearning: publish: %w", err)
		}
		if existing.Actual.SHA256 != info.Digests.SHA256 ||
			!maps.EqualFunc(oldProps, props, slices.Equal[[]string]) {
			return apierror.New(http.StatusConflict, "model version is immutable")
		}
		if _, err := ml.Verify(ctx, h.env(), c, id.Prefix(), m); err != nil {
			return fmt.Errorf("machinelearning: replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("machinelearning: publish: %w", err)
	}
	staged, err := ml.Verify(ctx, h.env(), c, id.Staging(tx), m)
	if err != nil {
		return h.missingFile("model upload is incomplete", err)
	}
	for i, file := range m.Files() {
		it := staged[i]
		blobInfo, err := h.k.Blobs().Stat(ctx, it.Actual.SHA256)
		if err != nil {
			return fmt.Errorf("machinelearning: publish: %w", err)
		}
		blobInfo.Digests = blob.Digests{
			SHA256: it.Actual.SHA256,
			SHA1:   it.Actual.SHA1,
			SHA512: it.Actual.SHA512,
			MD5:    it.Actual.MD5,
		}
		path := id.Prefix() + "/" + file.Path
		_, err = h.k.StoreNew(r, path, blobInfo, nil, types.Checksums{}, func() error {
			old, err := h.k.DB().Items().Get(ctx, c.Key, false, path)
			if err == nil && old.Actual.SHA256 == it.Actual.SHA256 && old.Size == it.Size {
				return nil
			}
			return apierror.New(
				http.StatusConflict,
				"model artifact already exists with different content",
			)
		})
		if err != nil {
			return fmt.Errorf("machinelearning: publish: %w", err)
		}
	}
	_, err = h.k.StoreNew(
		r,
		id.Manifest(),
		info,
		props,
		types.Checksums{},
		func() error { return apierror.New(http.StatusConflict, "model version already exists") },
	)
	if err != nil {
		return fmt.Errorf("machinelearning: publish: %w", err)
	}
	if err := (ml.Handler{}).Reindex(ctx, h.env(), c); err != nil {
		return fmt.Errorf("machinelearning: index publication: %w", err)
	}
	return nil
}
