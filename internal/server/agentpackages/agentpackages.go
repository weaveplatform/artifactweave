// Package agentpackages serves the APM v1 registry protocol on Agent Packages
// local and federated repositories. Federated peer convergence belongs to P13.
package agentpackages

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apmfmt "github.com/weaveplatform/artifactweave/internal/format/agentpackages"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func Protocol() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name: "agentpackages", Types: []repo.PackageType{repo.AgentPackages},
		ErrorProfile: apierror.ProfileAPM,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Get("/v1/packages/{owner}/{name}/versions", h.versions)
			r.Head("/v1/packages/{owner}/{name}/versions", h.versions)
			r.Get("/v1/packages/{owner}/{name}/versions/{version}/download", h.download)
			r.Head("/v1/packages/{owner}/{name}/versions/{version}/download", h.download)
			r.Put("/v1/packages/{owner}/{name}/versions/{version}", h.publish)
			r.Get("/v1/search", h.search)
			r.Head("/v1/search", h.search)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

var errInvalidIndex = errors.New("agentpackages: invalid agent package index metadata")

type version struct {
	Version     string `json:"version"`
	Digest      string `json:"digest"`
	PublishedAt string `json:"published_at"`
	SizeBytes   int64  `json:"size_bytes"`
}

func identity(r *http.Request) (string, string, string, error) {
	owner, name := chi.URLParam(r, "owner"), chi.URLParam(r, "name")
	version := chi.URLParam(r, "version")
	if !apmfmt.ValidComponent(owner) || !apmfmt.ValidComponent(name) ||
		version != "" && !apmfmt.ValidVersion(version) {
		return "", "", "", apierror.New(
			http.StatusBadRequest,
			"invalid Agent Package identity or version",
		)
	}
	return owner, name, version, nil
}

func (h handler) versions(w http.ResponseWriter, r *http.Request) {
	owner, name, _, err := identity(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !h.k.RequireFolder(w, r, owner+"/"+name, types.ActionRead) {
		return
	}
	p, err := h.k.DB().
		Packages().
		Find(r.Context(), pkgroute.RequestFrom(r).Repo.Key, owner+"/"+name)
	if errors.Is(err, store.ErrNotFound) {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "package not found"))
		return
	}
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("agentpackages: find package: %w", err))
		return
	}
	all, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("agentpackages: list versions: %w", err))
		return
	}
	answer := struct {
		Package  string    `json:"package"`
		Versions []version `json:"versions"`
	}{Package: p.Name, Versions: []version{}}
	for _, pv := range all {
		path := apmfmt.Path(owner, name, pv.Version)
		if !h.k.Can(r, path, types.ActionRead) {
			continue
		}
		var meta apmfmt.Metadata
		if json.Unmarshal(pv.Metadata, &meta) != nil || meta.SHA256 == "" {
			h.k.Error(w, r, fmt.Errorf("agentpackages: %s: %w", path, errInvalidIndex))
			return
		}
		answer.Versions = append(answer.Versions, version{
			Version: pv.Version, Digest: "sha256:" + meta.SHA256,
			PublishedAt: pv.Created.UTC().Format(time.RFC3339), SizeBytes: meta.Size,
		})
	}
	slices.Reverse(answer.Versions)
	if len(answer.Versions) == 0 {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "package not found"))
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, http.StatusOK, answer)
}

func (h handler) download(w http.ResponseWriter, r *http.Request) {
	owner, name, version, err := identity(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	path := apmfmt.Path(owner, name, version)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	h.k.ServeItem(w, r, path)
}

func (h handler) publish(w http.ResponseWriter, r *http.Request) {
	owner, name, version, err := identity(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if r.Header.Get("Content-Type") != "application/zip" {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusUnsupportedMediaType, "Agent Packages require application/zip"),
		)
		return
	}
	if r.ContentLength > apmfmt.MaxArchiveBytes {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusRequestEntityTooLarge, "Agent Package archive exceeds limit"),
		)
		return
	}
	path := apmfmt.Path(owner, name, version)
	if !h.k.Require(w, r, path, types.ActionWrite) {
		return
	}
	info, err := h.k.Upload(r, http.MaxBytesReader(w, r.Body, apmfmt.MaxArchiveBytes))
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	it, err := h.k.StoreNew(r, path, info, nil, pkgroute.DeclaredChecksums(r.Header),
		func() error { return apierror.New(http.StatusConflict, "version already published") })
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.Header().Set("Location", r.URL.Path)
	writeJSON(w, http.StatusCreated, struct {
		Package     string `json:"package"`
		Version     string `json:"version"`
		Digest      string `json:"digest"`
		PublishedAt string `json:"published_at"`
		SizeBytes   int64  `json:"size_bytes"`
	}{
		owner + "/" + name, version, "sha256:" + it.Actual.SHA256,
		it.Created.UTC().Format(time.RFC3339), it.Size,
	})
}

// search is JFrog's APM extension. The vendor guide names its filters but
// does not publish a response example; the result envelope remains [I] for
// a target-instance capture in #216.
func (h handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	tag := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag")))
	limit, err := nonnegative(r.URL.Query().Get("limit"), 20)
	if err != nil || limit > 100 {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid search limit"))
		return
	}
	offset, err := nonnegative(r.URL.Query().Get("offset"), 0)
	if err != nil {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid search offset"))
		return
	}
	answer := struct {
		Results []apmfmt.Metadata `json:"results"`
		Total   int               `json:"total"`
		Limit   int               `json:"limit"`
		Offset  int               `json:"offset"`
	}{Results: []apmfmt.Metadata{}, Limit: limit, Offset: offset}
	page := store.Page{Limit: store.MaxLimit}
	key := pkgroute.RequestFrom(r).Repo.Key
	for {
		pkgs, next, err := h.k.DB().Packages().List(r.Context(), key, page)
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("agentpackages: search packages: %w", err))
			return
		}
		for _, p := range pkgs {
			meta, ok, err := h.searchPackage(r, p, q, kind, tag)
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			if !ok {
				continue
			}
			answer.Total++
			if answer.Total <= offset || len(answer.Results) >= limit {
				continue
			}
			answer.Results = append(answer.Results, meta)
		}
		if next == "" {
			break
		}
		page.After = next
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, answer)
}

func (h handler) searchPackage(
	r *http.Request, p types.Package, q, kind, tag string,
) (apmfmt.Metadata, bool, error) {
	all, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return apmfmt.Metadata{}, false, fmt.Errorf(
			"agentpackages: versions of %s: %w",
			p.Name,
			err,
		)
	}
	for i := len(all) - 1; i >= 0; i-- {
		var meta apmfmt.Metadata
		if err := json.Unmarshal(all[i].Metadata, &meta); err != nil {
			return apmfmt.Metadata{}, false, fmt.Errorf(
				"agentpackages: metadata of %s: %w",
				p.Name,
				err,
			)
		}
		if !h.k.Can(r, meta.Path, types.ActionRead) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(meta.Name+" "+meta.Description), q) {
			return apmfmt.Metadata{}, false, nil
		}
		if kind != "" && !slices.ContainsFunc(meta.Types, func(v string) bool {
			return strings.EqualFold(v, kind)
		}) || tag != "" && !slices.ContainsFunc(meta.Tags, func(v string) bool {
			return strings.EqualFold(v, tag)
		}) {
			return apmfmt.Metadata{}, false, nil
		}
		return meta, true, nil
	}
	return apmfmt.Metadata{}, false, nil
}

func nonnegative(v string, defaultValue int) (int, error) {
	if v == "" {
		return defaultValue, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, apierror.New(http.StatusBadRequest, "invalid pagination")
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
