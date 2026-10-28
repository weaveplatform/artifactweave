// Package chef serves the Chef Supermarket cookbook protocol.
package chef

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

	cheffmt "github.com/weaveplatform/artifactweave/internal/format/chef"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	mediaType   = "application/json"
	maxVersions = 4096
)

type handler struct{ k *pkgroute.Kit }

func Protocol(remote *source.Service) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
		repo.Virtual:   pkgroute.Local{},
	}
	if remote != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: remote}
	}
	return pkgroute.Protocol{
		Name:         "chef",
		Types:        []repo.PackageType{repo.Chef},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources:      sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Get("/api/v1/cookbooks", h.catalog)
			r.Head("/api/v1/cookbooks", h.catalog)
			r.Get("/api/v1/search", h.catalog)
			r.Head("/api/v1/search", h.catalog)
			r.Post("/api/v1/cookbooks", h.publish)
			r.Get("/api/v1/cookbooks/{name}", h.cookbook)
			r.Head("/api/v1/cookbooks/{name}", h.cookbook)
			r.Post("/api/v1/cookbooks/{name}", h.publish)
			r.Delete("/api/v1/cookbooks/{name}", h.remove)
			r.Get("/api/v1/cookbooks/{name}/versions/{version}", h.version)
			r.Head("/api/v1/cookbooks/{name}/versions/{version}", h.version)
			r.Delete("/api/v1/cookbooks/{name}/versions/{version}", h.remove)
			r.Delete("/api/v1/cookbooks/{name}/{version}", h.remove)
			r.Get("/api/v1/cookbooks/{name}/versions/{version}/download", h.archive)
			r.Head("/api/v1/cookbooks/{name}/versions/{version}/download", h.archive)
		},
	}
}

type release struct {
	meta       cheffmt.Metadata
	item       types.Item
	member     *http.Request
	remoteURL  string
	revalidate func(context.Context) error
	checkRead  func(context.Context, string) error
}

func base(r *http.Request) string {
	return baseurl.From(
		r.Context(),
	) + "/artifactory/api/chef/" + url.PathEscape(
		pkgroute.RequestFrom(r).Repo.Key,
	)
}

func versionPath(name, version string) string {
	return "/api/v1/cookbooks/" + name + "/versions/" + strings.ReplaceAll(version, ".", "_")
}

func versionRecord(r *http.Request, m cheffmt.Metadata) map[string]any {
	return map[string]any{
		"version":           m.Version,
		"license":           m.Manifest.License,
		"dependencies":      nonNil(m.Manifest.Dependencies),
		"platforms":         nonNil(m.Manifest.Platforms),
		"tarball_file_size": m.Size,
		"sha256":            m.SHA256,
		"cookbook":          base(r) + "/api/v1/cookbooks/" + m.Name,
		"file":              base(r) + versionPath(m.Name, m.Version) + "/download",
	}
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func cookbookRecord(r *http.Request, found []release) map[string]any {
	m := found[0].meta
	versions := make([]string, 0, len(found))
	for _, v := range found {
		versions = append(versions, base(r)+versionPath(m.Name, v.meta.Version))
	}
	return map[string]any{
		"name":           m.Name,
		"maintainer":     m.Manifest.Maintainer,
		"description":    m.Manifest.Description,
		"category":       "Other",
		"latest_version": versions[0],
		"versions":       versions,
		"deprecated":     false,
		"external_url":   m.Manifest.SourceURL,
	}
}

func missing() error { return apierror.New(http.StatusNotFound, "Chef cookbook or version not found") }

func (h handler) identity(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := chi.URLParam(r, "name")
	if !cheffmt.ValidName(name) || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid cookbook request"))
		return "", false
	}
	return name, h.k.RequireFolder(w, r, name, types.ActionRead)
}

func (h handler) cookbook(w http.ResponseWriter, r *http.Request) {
	name, ok := h.identity(w, r)
	if !ok {
		return
	}
	found, err := h.lookup(r, name)
	if err == nil && len(found) == 0 {
		err = missing()
	}
	if err == nil {
		err = h.authority(r, found)
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, r, 200, cookbookRecord(r, found))
}

func (h handler) resolve(w http.ResponseWriter, r *http.Request) (release, bool) {
	name, ok := h.identity(w, r)
	if !ok {
		return release{}, false
	}
	version := strings.ReplaceAll(chi.URLParam(r, "version"), "_", ".")
	if version != "latest" && !cheffmt.ValidVersion(version) {
		h.k.Error(w, r, apierror.New(400, "invalid cookbook version"))
		return release{}, false
	}
	if version != "latest" && !h.k.Require(w, r, cheffmt.Path(name, version), types.ActionRead) {
		return release{}, false
	}
	found, err := h.lookup(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return release{}, false
	}
	for _, rel := range found {
		if version == "latest" || rel.meta.Version == version {
			if err = h.authority(r, []release{rel}); err != nil {
				h.k.Error(w, r, err)
				return release{}, false
			}
			return rel, true
		}
	}
	h.k.Error(w, r, missing())
	return release{}, false
}

func (h handler) version(w http.ResponseWriter, r *http.Request) {
	rel, ok := h.resolve(w, r)
	if ok {
		writeJSON(w, r, 200, versionRecord(r, rel.meta))
	}
}

func (h handler) archive(w http.ResponseWriter, r *http.Request) {
	rel, ok := h.resolve(w, r)
	if !ok {
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
	h.k.ServeItem(w, member, rel.meta.Path)
}

func (h handler) publish(w http.ResponseWriter, r *http.Request) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Local && c.Class != repo.Federated {
		h.k.Error(
			w,
			r,
			apierror.New(405, "cookbook publish requires a local or federated repository"),
		)
		return
	}
	name := strings.TrimSuffix(chi.URLParam(r, "name"), ".tgz")
	if name != "" && !cheffmt.ValidName(name) {
		h.k.Error(w, r, apierror.New(400, "invalid cookbook name"))
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) > 1 || len(query["category"]) > 1 ||
		len(query.Get("category")) > 256 ||
		len(query) == 1 && len(query["category"]) == 0 {
		h.k.Error(w, r, apierror.New(400, "invalid cookbook category"))
		return
	}
	if !h.k.RequireFolder(w, r, name, types.ActionWrite) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*cheffmt.MaxArchiveBytes)
	var upload pkgroute.Form
	if name == "" {
		upload, err = h.k.ReadFormLimited(r, "tarball", cheffmt.MaxArchiveBytes)
	} else {
		upload.File, err = h.k.Upload(r, r.Body)
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), upload.File.Digests.SHA256)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	meta, err := cheffmt.ReadArchive(rc, upload.File.Size)
	_ = rc.Close()
	if err != nil || name != "" && meta.Name != name {
		h.k.Error(w, r, apierror.New(400, "invalid cookbook archive or identity"))
		return
	}
	_, err = h.k.StoreNew(
		r,
		cheffmt.Path(meta.Name, meta.Version),
		upload.File,
		nil,
		types.Checksums{},
		func() error { return apierror.New(409, "cookbook version already exists") },
	)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	meta.SHA256 = upload.File.Digests.SHA256
	writeJSON(w, r, 200, versionRecord(r, meta))
}

func (h handler) remove(w http.ResponseWriter, r *http.Request) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Local && c.Class != repo.Federated {
		h.k.Error(
			w,
			r,
			apierror.New(405, "cookbook delete requires a local or federated repository"),
		)
		return
	}
	name := chi.URLParam(r, "name")
	version := strings.ReplaceAll(chi.URLParam(r, "version"), "_", ".")
	if !cheffmt.ValidName(name) || version != "" && !cheffmt.ValidVersion(version) ||
		r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid cookbook delete"))
		return
	}
	var err error
	if version == "" {
		err = h.k.DeleteTree(r, name)
	} else {
		err = h.k.Delete(r, cheffmt.Path(name, version))
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, r, 200, map[string]string{"name": name, "version": version})
}

func (h handler) catalog(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.k.Error(w, r, apierror.New(400, "invalid search query"))
		return
	}
	for key, values := range q {
		if len(values) != 1 || key != "start" && key != "items" && key != "q" {
			h.k.Error(w, r, apierror.New(400, "unsupported search query"))
			return
		}
	}
	start, limit := 0, 1000
	if q.Has("start") {
		start, err = strconv.Atoi(q.Get("start"))
	}
	if err == nil && q.Has("items") {
		limit, err = strconv.Atoi(q.Get("items"))
	}
	query := q.Get("q")
	if err != nil || start < 0 || start > maxVersions || limit < 1 || limit > 1000 ||
		len(query) > 128 {
		h.k.Error(w, r, apierror.New(400, "invalid search bounds"))
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	names, err := h.names(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	out := []map[string]any{}
	checks := []release{}
	for _, name := range names {
		if query != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(query)) ||
			!h.k.CanFolder(r, name, types.ActionRead) {
			continue
		}
		found, e := h.lookup(r, name)
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		if len(found) == 0 {
			continue
		}
		checks = append(checks, found...)
		m := found[0].meta
		out = append(
			out,
			map[string]any{
				"cookbook_name":        name,
				"cookbook_description": m.Manifest.Description,
				"cookbook_maintainer":  m.Manifest.Maintainer,
				"cookbook":             base(r) + "/api/v1/cookbooks/" + name,
			},
		)
	}
	if err = h.authority(r, checks); err != nil {
		h.k.Error(w, r, err)
		return
	}
	total := len(out)
	offset := min(start, total)
	out = out[offset:min(offset+limit, total)]
	writeJSON(w, r, 200, map[string]any{"total": total, "start": start, "items": out})
}

func (h handler) names(r *http.Request) ([]string, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		return h.remoteNames(r)
	}
	if c.Class == repo.Virtual {
		unique := map[string]bool{}
		for _, key := range c.Repositories {
			member, err := h.k.DB().Repos().Get(r.Context(), key)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("chef: member: %w", err)
			}
			mr, err := h.k.MemberRequest(r, member)
			if err != nil {
				return nil, fmt.Errorf("chef: member request: %w", err)
			}
			if !h.k.CanFolder(mr, "", types.ActionRead) {
				continue
			}
			names, err := h.names(mr)
			if err != nil {
				return nil, err
			}
			for _, n := range names {
				unique[n] = true
			}
			if len(unique) > maxVersions {
				return nil, apierror.New(503, "cookbook catalog exceeds limit")
			}
		}
		out := make([]string, 0, len(unique))
		for name := range unique {
			out = append(out, name)
		}
		slices.Sort(out)
		return out, nil
	}
	out := []string{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		packages, next, err := h.k.DB().Packages().List(r.Context(), c.Key, page)
		if err != nil {
			return nil, fmt.Errorf("chef: catalog: %w", err)
		}
		for _, p := range packages {
			out = append(out, p.Name)
		}
		if len(out) > maxVersions {
			return nil, apierror.New(503, "cookbook catalog exceeds limit")
		}
		if next == "" {
			break
		}
		page.After = next
	}
	return out, nil
}

func (h handler) authority(r *http.Request, releases []release) error {
	for _, rel := range releases {
		if rel.revalidate != nil {
			if err := rel.revalidate(r.Context()); err != nil {
				return fmt.Errorf("chef: authority: %w", pkgroute.SourceError(err, false))
			}
		}
		if rel.checkRead != nil {
			if err := rel.checkRead(r.Context(), rel.meta.Path); err != nil {
				return fmt.Errorf("chef: authority: %w", pkgroute.SourceError(err, false))
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
		return nil, fmt.Errorf("chef: find package: %w", err)
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return nil, fmt.Errorf("chef: package versions: %w", err)
	}
	if len(versions) > maxVersions {
		return nil, apierror.New(503, "Chef releases exceed limit")
	}
	out := make([]release, 0, len(versions))
	for _, v := range versions {
		path := cheffmt.Path(name, v.Version)
		if !h.k.Can(r, path, types.ActionRead) ||
			generic.CheckDownload(q.Repo, path, false) != nil {
			continue
		}
		var meta cheffmt.Metadata
		if json.Unmarshal(v.Metadata, &meta) != nil || meta.Name != name ||
			meta.Version != v.Version || meta.Path != path {
			return nil, apierror.New(http.StatusBadGateway, "invalid stored Chef package index")
		}
		item, err := h.k.DB().Items().Get(r.Context(), q.Repo.Key, false, path)
		if err != nil || item.Folder || meta.SHA256 == "" || meta.SHA256 != item.Actual.SHA256 {
			return nil, apierror.New(http.StatusBadGateway, "invalid stored Chef archive")
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
			return nil, fmt.Errorf("chef: virtual member: %w", err)
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
		return nil, apierror.New(503, "Chef releases exceed limit")
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
