// Package puppet serves the Forge v3 module protocol.
package puppet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/format/generic"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/format/puppet"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const mediaType = "application/json"

type Options struct {
	Blobs         blob.Store
	RemoteSources *source.Service
}

type handler struct {
	k     *pkgroute.Kit
	blobs blob.Store
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

func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
		repo.Virtual:   virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name:         "puppet",
		Types:        []repo.PackageType{repo.Puppet},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources:      sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, blobs: o.Blobs}
			r.Get("/v3/releases", h.releases)
			r.Head("/v3/releases", h.releases)
			r.Get("/v3/releases/{slug}", h.release)
			r.Head("/v3/releases/{slug}", h.release)
			r.Get("/v3/modules/{name}", h.module)
			r.Head("/v3/modules/{name}", h.module)
			r.Get("/v3/modules", h.modules)
			r.Head("/v3/modules", h.modules)
			r.Get("/v3/files/{file}", h.archive)
			r.Head("/v3/files/{file}", h.archive)
		},
	}
}

type release struct {
	meta       puppetfmt.Metadata
	item       types.Item
	member     *http.Request
	remoteURL  string
	revalidate func(context.Context) error
	checkRead  func(context.Context, string) error
}

type versionRecord struct {
	URI        string             `json:"uri"`
	Slug       string             `json:"slug"`
	Version    string             `json:"version"`
	FileURI    string             `json:"file_uri"`
	FileSHA256 string             `json:"file_sha256"`
	FileSize   int64              `json:"file_size"`
	Metadata   puppetfmt.Manifest `json:"metadata"`
	Tags       []string           `json:"tags"`
}
type pagination struct {
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
	Total  int     `json:"total"`
	Next   *string `json:"next"`
}

func record(meta puppetfmt.Metadata) versionRecord {
	slug := meta.Name + "-" + meta.Version
	return versionRecord{
		URI:        "/v3/releases/" + slug,
		Slug:       slug,
		Version:    meta.Version,
		FileURI:    "/v3/files/" + slug + ".tar.gz",
		FileSHA256: meta.SHA256,
		FileSize:   meta.Size,
		Metadata:   meta.Manifest,
		Tags:       []string{},
	}
}

func splitSlug(slug string) (string, string, bool) {
	owner, rest, ok := strings.Cut(slug, "-")
	if !ok {
		return "", "", false
	}
	module, version, ok := strings.Cut(rest, "-")
	name := owner + "-" + module
	return name, version, ok && puppetfmt.ValidName(name) && puppetfmt.ValidVersion(version)
}

func (h handler) releases(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 4096 {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet query"))
		return
	}
	for key, values := range query {
		if len(values) != 1 ||
			!slices.Contains(
				[]string{"module", "sort_by", "exclude_fields", "limit", "offset"},
				key,
			) {
			h.k.Error(w, r, apierror.New(400, "unsupported Puppet query"))
			return
		}
	}
	name := query.Get("module")
	if !puppetfmt.ValidName(name) ||
		query.Get("sort_by") != "" && query.Get("sort_by") != "version" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet module"))
		return
	}
	limit, offset := 1000, 0
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
	}
	if err != nil || limit < 1 || limit > 1000 {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet limit"))
		return
	}
	if query.Has("offset") {
		offset, err = strconv.Atoi(query.Get("offset"))
	}
	if err != nil || offset < 0 || offset > maxVersions {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet offset"))
		return
	}
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if err = h.authority(r, releases); err != nil {
		h.k.Error(w, r, err)
		return
	}
	out := []versionRecord{}
	end := min(offset+limit, len(releases))
	for i := offset; i < end; i++ {
		out = append(out, record(releases[i].meta))
	}
	page := pagination{Limit: limit, Offset: offset, Total: len(releases)}
	if end < len(releases) {
		next := "/v3/releases?module=" + name + "&limit=" + strconv.Itoa(
			limit,
		) + "&offset=" + strconv.Itoa(
			end,
		)
		page.Next = &next
	}
	writeJSON(w, r, 200, struct {
		Results    []versionRecord `json:"results"`
		Pagination pagination      `json:"pagination"`
	}{out, page})
}

func (h handler) release(w http.ResponseWriter, r *http.Request) {
	name, version, ok := splitSlug(chi.URLParam(r, "slug"))
	if !ok || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet release"))
		return
	}
	if !h.k.Require(w, r, puppetfmt.Path(name, version), types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, rel := range releases {
		if rel.meta.Version == version {
			if err = h.authority(r, []release{rel}); err != nil {
				h.k.Error(w, r, err)
				return
			}
			writeJSON(w, r, 200, record(rel.meta))
			return
		}
	}
	h.k.Error(w, r, apierror.New(404, "Puppet release not found"))
}

func moduleRecord(releases []release) map[string]any {
	name := releases[0].meta.Name
	owner, module, _ := strings.Cut(name, "-")
	versions := make([]map[string]string, 0, len(releases))
	for _, rel := range releases {
		versions = append(
			versions,
			map[string]string{
				"version": rel.meta.Version,
				"uri":     "/v3/releases/" + name + "-" + rel.meta.Version,
			},
		)
	}
	return map[string]any{
		"name":            module,
		"slug":            name,
		"uri":             "/v3/modules/" + name,
		"owner":           map[string]string{"username": owner},
		"current_release": record(releases[0].meta),
		"releases":        versions,
	}
}

func (h handler) module(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !puppetfmt.ValidName(name) || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet module"))
		return
	}
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(releases) == 0 {
		h.k.Error(w, r, apierror.New(404, "Puppet module not found"))
		return
	}
	if err = h.authority(r, releases); err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, r, 200, moduleRecord(releases))
}

// Search is deliberately exact-name and bounded; it does not proxy arbitrary queries.
func (h handler) modules(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q["query"]) != 1 || !puppetfmt.ValidName(q.Get("query")) {
		h.k.Error(w, r, apierror.New(400, "search requires an exact Puppet module name"))
		return
	}
	name := q.Get("query")
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if err = h.authority(r, releases); err != nil {
		h.k.Error(w, r, err)
		return
	}
	out := []map[string]any{}
	if len(releases) > 0 {
		out = append(out, moduleRecord(releases))
	}
	writeJSON(
		w,
		r,
		200,
		map[string]any{"results": out, "pagination": pagination{Limit: 1000, Total: len(out)}},
	)
}

func (h handler) archive(w http.ResponseWriter, r *http.Request) {
	file := chi.URLParam(r, "file")
	name, version, ok := splitSlug(strings.TrimSuffix(file, ".tar.gz"))
	if !ok || !strings.HasSuffix(file, ".tar.gz") || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet archive"))
		return
	}
	path := puppetfmt.Path(name, version)
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, rel := range releases {
		if rel.meta.Version == version {
			if err = h.authority(r, []release{rel}); err != nil {
				h.k.Error(w, r, err)
				return
			}
			if rel.remoteURL != "" {
				h.remoteArchive(w, r, rel)
				return
			}
			member := r
			if rel.member != nil {
				member = rel.member
			}
			h.k.ServeItem(w, member, path)
			return
		}
	}
	h.k.Error(w, r, apierror.New(404, "Puppet release not found"))
}

func (h handler) authority(r *http.Request, releases []release) error {
	for _, rel := range releases {
		if rel.revalidate != nil {
			if err := rel.revalidate(r.Context()); err != nil {
				return fmt.Errorf("puppet: authority: %w", pkgroute.SourceError(err, false))
			}
		}
		if rel.checkRead != nil {
			if err := rel.checkRead(r.Context(), rel.meta.Path); err != nil {
				return fmt.Errorf("puppet: authority: %w", pkgroute.SourceError(err, false))
			}
		}
	}
	return nil
}

func (h handler) lookup(r *http.Request, name string) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	switch q.Repo.Class {
	case repo.Local, repo.Federated:
		return h.hosted(r, name)
	case repo.Remote:
		return h.remoteVersions(r, name)
	case repo.Virtual:
		return h.virtual(r, name)
	default:
		return nil, source.ErrUnsupported
	}
}

func (h handler) hosted(r *http.Request, name string) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	p, err := h.k.DB().Packages().Find(r.Context(), q.Repo.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("puppet: find package: %w", err)
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return nil, fmt.Errorf("puppet: package versions: %w", err)
	}
	if len(versions) > maxVersions {
		return nil, apierror.New(503, "Puppet releases exceed limit")
	}
	out := make([]release, 0, len(versions))
	for _, v := range versions {
		path := puppetfmt.Path(name, v.Version)
		if !h.k.Can(r, path, types.ActionRead) ||
			generic.CheckDownload(q.Repo, path, false) != nil {
			continue
		}
		var meta puppetfmt.Metadata
		if json.Unmarshal(v.Metadata, &meta) != nil || meta.Name != name ||
			meta.Version != v.Version || meta.Path != path {
			return nil, apierror.New(http.StatusBadGateway, "invalid stored Puppet package index")
		}
		item, err := h.k.DB().Items().Get(r.Context(), q.Repo.Key, false, path)
		if err != nil || item.Folder || meta.SHA256 == "" || meta.SHA256 != item.Actual.SHA256 {
			return nil, apierror.New(http.StatusBadGateway, "invalid stored Puppet archive")
		}
		out = append(out, release{meta: meta, item: item})
	}
	sortReleases(out)
	return out, nil
}

func (h handler) virtual(r *http.Request, name string) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	byVersion := map[string]release{}
	for _, key := range q.Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("puppet: virtual member: %w", err)
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, err //nolint:wrapcheck // protocol class refusal
		}
		if !h.k.CanFolder(mr, name, types.ActionRead) {
			continue
		}
		found, err := h.lookup(mr, name)
		if err != nil {
			var response *apierror.Error
			if errors.Is(err, store.ErrNotFound) ||
				errors.As(err, &response) && response.Status == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		for _, v := range found {
			if !h.k.Can(r, v.meta.Path, types.ActionRead) ||
				generic.CheckDownload(q.Repo, v.meta.Path, false) != nil {
				continue
			}
			if _, exists := byVersion[v.meta.Version]; !exists {
				v.member = mr
				byVersion[v.meta.Version] = v
			}
		}
	}
	if len(byVersion) > maxVersions {
		return nil, apierror.New(503, "Puppet releases exceed limit")
	}
	out := make([]release, 0, len(byVersion))
	for _, v := range byVersion {
		out = append(out, v)
	}
	sortReleases(out)
	return out, nil
}

func sortReleases(out []release) {
	slices.SortFunc(out, func(a, b release) int {
		cmp := semver.Compare("v"+a.meta.Version, "v"+b.meta.Version)
		if cmp != 0 {
			return -cmp
		}
		return strings.Compare(a.meta.Version, b.meta.Version)
	})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, doc any) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(doc)
	}
}
