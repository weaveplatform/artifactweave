// Package gitlfs serves Git LFS batch and basic object transfers.
package gitlfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	baseformat "github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	format "github.com/weaveplatform/artifactweave/internal/format/gitlfs"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	mediaType      = "application/vnd.git-lfs+json"
	maxBatchBytes  = 1 << 20
	maxBatchItems  = 1000
	maxObjectBytes = int64(math.MaxInt64 - 1)
)

type Options struct{ RemoteSources *source.Service }

func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name: "lfs", Types: []repo.PackageType{repo.GitLFS},
		Surface: apierror.Artifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, o: o}
			r.Post("/objects/batch", h.batch)
			r.Get("/objects/{oid}", h.download)
			r.Head("/objects/{oid}", h.download)
			r.Put("/objects/{oid}", h.upload)
			r.Post("/locks", h.createLock)
			r.Get("/locks", h.listLocks)
			r.Post("/locks/verify", h.verifyLocks)
			r.Post("/locks/{id}/unlock", h.unlockLock)
		},
	}
}

type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	store.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

type handler struct {
	k *pkgroute.Kit
	o Options
}

type batchObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type batchRequest struct {
	Operation string        `json:"operation"`
	Transfers []string      `json:"transfers,omitempty"`
	Objects   []batchObject `json:"objects"`
	HashAlgo  string        `json:"hash_algo,omitempty"`
	Ref       *struct {
		Name string `json:"name"`
	} `json:"ref,omitempty"`
}

type batchAction struct {
	Href string `json:"href"`
}

type objectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type batchResult struct {
	OID     string                 `json:"oid"`
	Size    int64                  `json:"size"`
	Actions map[string]batchAction `json:"actions,omitempty"`
	Error   *objectError           `json:"error,omitempty"`
}

type batchResponse struct {
	Transfer string        `json:"transfer"`
	Objects  []batchResult `json:"objects"`
	HashAlgo string        `json:"hash_algo"`
}

func validMediaType(value string) bool {
	media, params, err := mime.ParseMediaType(value)
	return err == nil && media == mediaType &&
		(len(params) == 0 || len(params) == 1 && strings.EqualFold(params["charset"], "utf-8"))
}

func (h handler) batch(w http.ResponseWriter, r *http.Request) {
	if !validMediaType(r.Header.Get("Content-Type")) ||
		r.Header.Get("Accept") != "" && !strings.Contains(r.Header.Get("Accept"), mediaType) ||
		r.URL.RawQuery != "" {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusUnsupportedMediaType, "invalid Git LFS batch media type"),
		)
		return
	}
	var request batchRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxBatchBytes)
	dec := json.NewDecoder(r.Body)
	if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF ||
		(request.Operation != "upload" && request.Operation != "download") ||
		(request.HashAlgo != "" && request.HashAlgo != "sha256") ||
		len(request.Objects) == 0 || len(request.Objects) > maxBatchItems ||
		(len(request.Transfers) > 0 && !slices.Contains(request.Transfers, "basic")) {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Git LFS batch"))
		return
	}
	for _, obj := range request.Objects {
		if _, ok := format.ObjectPath(obj.OID); !ok || obj.Size < 0 || obj.Size > maxObjectBytes {
			h.k.Error(w, r, apierror.New(http.StatusUnprocessableEntity, "invalid Git LFS object"))
			return
		}
	}
	result := batchResponse{
		Transfer: "basic",
		HashAlgo: "sha256",
		Objects:  make([]batchResult, 0, len(request.Objects)),
	}
	for _, obj := range request.Objects {
		entry := batchResult{OID: obj.OID, Size: obj.Size}
		var err error
		switch request.Operation {
		case "download":
			err = h.downloadEntry(r, obj, &entry)
		case "upload":
			err = h.uploadEntry(r, obj, &entry)
		}
		if err != nil {
			entry.Error = &objectError{Code: statusOf(err), Message: "Git LFS object unavailable"}
		}
		result.Objects = append(result.Objects, entry)
	}
	w.Header().Set("Content-Type", mediaType)
	_ = json.NewEncoder(w).Encode(result)
}

func statusOf(err error) int {
	var api *apierror.Error
	var rejected *baseformat.Rejection
	if errors.As(err, &api) {
		return api.Status
	}
	if errors.As(err, &rejected) {
		return rejected.Status
	}
	if errors.Is(err, auth.ErrForbidden) || errors.Is(err, source.ErrDenied) {
		return http.StatusForbidden
	}
	if errors.Is(err, auth.ErrUnauthenticated) {
		return http.StatusUnauthorized
	}
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, store.ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusBadGateway
}

func objectURL(r *http.Request, oid string, size int64) string {
	u := baseurl.From(r.Context()) + "/artifactory/api/lfs/" +
		url.PathEscape(pkgroute.RequestFrom(r).Repo.Key) + "/objects/" + oid
	if size >= 0 {
		u += "?size=" + strconv.FormatInt(size, 10)
	}
	return u
}

func (h handler) uploadEntry(r *http.Request, obj batchObject, out *batchResult) error {
	path, _ := format.ObjectPath(obj.OID)
	target, err := h.targetRequest(r, path)
	if err != nil {
		return err
	}
	// A write-only principal may upload an already present OID. The batch
	// response must not reveal whether that OID exists or its stored size.
	if !h.k.Can(target, path, types.ActionRead) {
		out.Actions = map[string]batchAction{"upload": {Href: objectURL(r, obj.OID, obj.Size)}}
		return nil
	}
	found, err := h.k.Item(target, path)
	if err == nil {
		if !found.Item.Folder && found.Item.Size == obj.Size &&
			found.Item.Actual.SHA256 == obj.OID {
			return nil
		}
		return apierror.New(http.StatusConflict, "Git LFS object identity conflict")
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("gitlfs: check existing object: %w", err)
	}
	out.Actions = map[string]batchAction{"upload": {Href: objectURL(r, obj.OID, obj.Size)}}
	return nil
}

func (h handler) downloadEntry(r *http.Request, obj batchObject, out *batchResult) error {
	if err := h.available(r, obj); err != nil {
		return err
	}
	out.Actions = map[string]batchAction{"download": {Href: objectURL(r, obj.OID, obj.Size)}}
	return nil
}

func (h handler) available(r *http.Request, obj batchObject) error {
	path, _ := format.ObjectPath(obj.OID)
	if err := generic.CheckDownload(pkgroute.RequestFrom(r).Repo, path, false); err != nil {
		return fmt.Errorf("gitlfs: download policy: %w", err)
	}
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		return h.remoteAvailable(r, obj)
	case repo.Virtual:
		members, err := h.members(r)
		if err != nil {
			return err
		}
		readable := false
		for _, member := range members {
			if !h.k.Can(member, path, types.ActionRead) {
				continue
			}
			readable = true
			if err = h.available(member, obj); missing(err) {
				continue
			}
			return err
		}
		if !readable {
			return apierror.New(http.StatusForbidden, "Git LFS read denied")
		}
		return store.ErrNotFound
	default:
		found, err := h.find(r, path)
		if err != nil {
			return err
		}
		if found.Item.Folder || found.Item.Size != obj.Size || found.Item.Actual.SHA256 != obj.OID {
			return apierror.New(http.StatusUnprocessableEntity, "Git LFS object integrity mismatch")
		}
		return nil
	}
}

func (h handler) targetRequest(r *http.Request, path string) (*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		return nil, apierror.New(
			http.StatusMethodNotAllowed,
			"remote Git LFS repository is read-only",
		)
	}
	if c.Class != repo.Virtual && !h.k.Can(r, path, types.ActionWrite) {
		return nil, apierror.New(http.StatusForbidden, "Git LFS upload denied")
	}
	if c.Class != repo.Virtual {
		return r, nil
	}
	if c.DefaultDeploymentRepo == "" || !slices.Contains(c.Repositories, c.DefaultDeploymentRepo) {
		return nil, apierror.New(
			http.StatusMethodNotAllowed,
			"virtual Git LFS deployment target is missing",
		)
	}
	member, err := h.k.DB().Repos().Get(r.Context(), c.DefaultDeploymentRepo)
	if err != nil {
		return nil, fmt.Errorf("gitlfs: deployment member: %w", err)
	}
	if member.PackageType != repo.GitLFS ||
		member.Class != repo.Local && member.Class != repo.Federated {
		return nil, apierror.New(http.StatusBadRequest, "invalid Git LFS deployment target")
	}
	mr, err := h.k.MemberRequest(r, member)
	if err != nil {
		return nil, fmt.Errorf("gitlfs: deployment request: %w", err)
	}
	if !h.k.Can(mr, path, types.ActionWrite) {
		return nil, apierror.New(http.StatusForbidden, "Git LFS deployment target denied")
	}
	return mr, nil
}

func (h handler) members(r *http.Request) ([]*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	seen := map[string]bool{c.Key: true}
	result := make([]*http.Request, 0, len(c.Repositories))
	for _, key := range c.Repositories {
		if seen[key] {
			return nil, apierror.New(http.StatusBadRequest, "invalid Git LFS virtual member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("gitlfs: virtual member: %w", err)
		}
		if member.PackageType != repo.GitLFS ||
			member.Class != repo.Local && member.Class != repo.Federated &&
				member.Class != repo.Remote {
			return nil, apierror.New(http.StatusBadRequest, "unsupported Git LFS member")
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("gitlfs: member request: %w", err)
		}
		result = append(result, mr)
	}
	return result, nil
}

func (h handler) find(r *http.Request, path string) (pkgroute.Found, error) {
	// Virtual reads select a permitted member in available or serveObject,
	// then call find with that member request. A virtual key itself is never a
	// permission target.
	if !h.k.Can(r, path, types.ActionRead) {
		return pkgroute.Found{}, apierror.New(http.StatusForbidden, "Git LFS read denied")
	}
	if err := generic.CheckDownload(pkgroute.RequestFrom(r).Repo, path, false); err != nil {
		return pkgroute.Found{}, fmt.Errorf("gitlfs: download policy: %w", err)
	}
	found, err := h.k.Item(r, path)
	if err != nil {
		return pkgroute.Found{}, fmt.Errorf("gitlfs: find object: %w", err)
	}
	return found, nil
}

func (h handler) download(w http.ResponseWriter, r *http.Request) {
	oid := chi.URLParam(r, "oid")
	path, ok := format.ObjectPath(oid)
	query := r.URL.Query()
	size := int64(-1)
	if r.URL.RawQuery != "" {
		var err error
		size, err = strconv.ParseInt(query.Get("size"), 10, 64)
		if err != nil || len(query) != 1 || len(query["size"]) != 1 || size < 0 ||
			size > maxObjectBytes {
			ok = false
		}
	}
	if !ok {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Git LFS object path"))
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote ||
		pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		if size < 0 {
			h.k.Error(
				w,
				r,
				apierror.New(http.StatusBadRequest, "Git LFS transfer size is required"),
			)
			return
		}
		if err := h.serveObject(w, r, batchObject{OID: oid, Size: size}); err != nil {
			h.objectError(w, r, err)
		}
		return
	}
	found, err := h.find(r, path)
	if err != nil {
		h.objectError(w, r, err)
		return
	}
	if found.Item.Folder || found.Item.Actual.SHA256 != oid ||
		size >= 0 && found.Item.Size != size {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Git LFS object unavailable"))
		return
	}
	h.k.ServeFound(w, r, found)
}

func (h handler) objectError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		err = apierror.New(http.StatusNotFound, "Git LFS object unavailable")
	}
	h.k.Error(w, r, err)
}

func (h handler) serveObject(w http.ResponseWriter, r *http.Request, obj batchObject) error {
	path, _ := format.ObjectPath(obj.OID)
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		result, err := h.remoteObject(r, obj)
		if err != nil {
			return err
		}
		h.k.ServeResult(w, r, result)
		return nil
	case repo.Virtual:
		members, err := h.members(r)
		if err != nil {
			return err
		}
		readable := false
		for _, member := range members {
			if !h.k.Can(member, path, types.ActionRead) {
				continue
			}
			readable = true
			if err = h.available(member, obj); missing(err) {
				continue
			}
			if err != nil {
				return err
			}
			if pkgroute.RequestFrom(member).Repo.Class == repo.Remote {
				return h.serveObject(w, member, obj)
			}
			found, err := h.find(member, path)
			if err != nil {
				return err
			}
			h.k.ServeFound(w, member, found)
			return nil
		}
		if !readable {
			return apierror.New(http.StatusForbidden, "Git LFS read denied")
		}
		return store.ErrNotFound
	default:
		return source.ErrUnsupported
	}
}

func (h handler) upload(w http.ResponseWriter, r *http.Request) {
	oid := chi.URLParam(r, "oid")
	path, ok := format.ObjectPath(oid)
	size, err := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	if !ok || err != nil || len(r.URL.Query()) != 1 || len(r.URL.Query()["size"]) != 1 ||
		size < 0 || size > maxObjectBytes ||
		(r.ContentLength >= 0 && r.ContentLength != size) {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Git LFS upload"))
		return
	}
	target, err := h.targetRequest(r, path)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, size+1)
	info, err := h.k.Upload(target, r.Body)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if info.Size != size || info.Digests.SHA256 != oid {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusUnprocessableEntity, "Git LFS object integrity mismatch"),
		)
		return
	}
	_, err = h.k.StoreNew(target, path, info, nil, types.Checksums{SHA256: oid},
		func() error { return apierror.New(http.StatusConflict, "Git LFS object already exists") })
	if err != nil {
		if statusOf(err) != http.StatusConflict {
			h.k.Error(w, r, err)
			return
		}
		found, getErr := h.k.Item(target, path)
		if getErr == nil && !found.Item.Folder && found.Item.Size == size &&
			found.Item.Actual.SHA256 == oid {
			w.WriteHeader(http.StatusOK)
			return
		}
		h.k.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
