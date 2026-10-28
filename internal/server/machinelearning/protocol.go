// Package machinelearning implements the FrogML SDK's Artifactory model
// transaction and manifest API on local and one-node federated repositories.
package machinelearning

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func Protocol() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:         "machinelearning",
		Types:        []repo.PackageType{repo.MachineLearning},
		ErrorProfile: apierror.ProfileArtifactory,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			for _, name := range []string{"/{name}", "/{namespace}/{name}"} {
				r.Post("/model"+name+"/start-transaction", h.start)
				r.Post("/model"+name+"/start-transaction/{version}", h.start)
				r.Put("/model/entity-manifest"+name+"/{version}/{transaction}", h.commit)
				r.Get("/model/entity-manifest"+name+"/{version}", h.manifest)
			}
		},
	}
}

type handler struct{ k *pkgroute.Kit }

var errTransactionCollision = errors.New("transaction identifier already exists")

func identity(r *http.Request, autoVersion bool) (ml.Identity, error) {
	name, err := url.PathUnescape(chi.URLParam(r, "name"))
	if err != nil {
		return ml.Identity{}, badRequest("invalid model name")
	}
	if namespace := chi.URLParam(r, "namespace"); namespace != "" {
		ns, err := url.PathUnescape(namespace)
		if err != nil || !ml.Segment(ns) {
			return ml.Identity{}, badRequest("invalid model namespace")
		}
		name = ns + "/" + name
	}
	version, err := url.PathUnescape(chi.URLParam(r, "version"))
	if err != nil {
		return ml.Identity{}, badRequest("invalid model version")
	}
	if version == "" && autoVersion {
		version = strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	if !ml.Name(name) || !ml.Version(version) || r.URL.RawQuery != "" {
		return ml.Identity{}, badRequest("invalid model identity")
	}
	return ml.Identity{Name: name, Version: version}, nil
}

func (h handler) env() format.Env {
	return format.Env{Store: h.k.DB(), Blobs: h.k.Blobs(), Now: time.Now}
}
func badRequest(message string) error { return apierror.New(http.StatusBadRequest, message) }

func (h handler) start(w http.ResponseWriter, r *http.Request) {
	id, err := identity(r, true)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Anonymous {
		h.fail(w, r, apierror.New(http.StatusUnauthorized, "authentication required"))
		return
	}
	if !h.k.RequireFolder(w, r, id.Prefix(), types.ActionWrite) {
		return
	}
	if _, err := h.k.DB().
		Items().
		Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, id.Manifest()); err == nil {
		h.fail(w, r, apierror.New(http.StatusConflict, "model version already exists"))
		return
	} else if !errors.Is(
		err,
		store.ErrNotFound,
	) {
		h.fail(w, r, err)
		return
	}
	request := r.WithContext(ml.Publication(r.Context()))
	info, err := h.k.Upload(request, bytes.NewReader([]byte("{}")))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	for offset := range int64(32) {
		tx := strconv.FormatInt(time.Now().UnixMilli()+offset, 10)
		path := id.Staging(tx)
		_, err := h.k.StoreNew(
			request,
			path+"/"+ml.TransactionName,
			info,
			nil,
			types.Checksums{},
			func() error { return errTransactionCollision },
		)
		if errors.Is(err, errTransactionCollision) {
			continue
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(
			w,
			http.StatusOK,
			map[string]string{
				"filesUploadPath":        path + "/files",
				"leadUploadPath":         path,
				"dependenciesUploadPath": path + "/dependencies",
				"codeUploadPath":         path + "/code",
				"transactionId":          tx,
			},
		)
		return
	}
	h.fail(w, r, apierror.New(http.StatusConflict, "retry starting the model upload"))
}

func (h handler) manifest(w http.ResponseWriter, r *http.Request) {
	id, err := identity(r, false)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.k.Require(w, r, id.Manifest(), types.ActionRead) {
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	m, _, err := ml.Read(r.Context(), h.env(), c, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	files, err := ml.Verify(r.Context(), h.env(), c, id.Prefix(), m)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	for _, it := range files {
		if !h.k.Require(w, r, it.FullPath(), types.ActionRead) {
			return
		}
	}
	base := baseurl.From(
		r.Context(),
	) + "/artifactory/" + url.PathEscape(
		c.Key,
	) + "/" + escapePath(
		id.Prefix(),
	)
	for i := range m.Artifacts {
		m.Artifacts[i].DownloadURL = base + "/files/" + escapePath(m.Artifacts[i].Path)
	}
	for i := range m.Dependencies {
		m.Dependencies[i].DownloadURL = base + "/dependencies/" + escapePath(m.Dependencies[i].Path)
	}
	if m.Code != nil {
		m.Code.DownloadURL = base + "/code/" + escapePath(m.Code.Path)
	}
	writeJSON(w, http.StatusOK, m)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func escapePath(p string) string {
	u := url.URL{Path: p}
	return u.EscapedPath()
}

func (h handler) missingFile(message string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return apierror.New(http.StatusConflict, message)
	}
	return fmt.Errorf("machinelearning: %s: %w", message, err)
}

func (h handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var rejection *format.RejectionError
	if errors.As(err, &rejection) {
		err = apierror.New(rejection.Status, rejection.Message)
	}
	if errors.Is(err, store.ErrNotFound) {
		err = apierror.New(http.StatusNotFound, "model or upload transaction not found")
	}
	h.k.Error(w, r, err)
}
