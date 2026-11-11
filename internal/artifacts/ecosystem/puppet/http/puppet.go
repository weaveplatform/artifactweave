package puppethttp

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

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const mediaType = "application/json"

// Options configures the Puppet protocol. Blobs opens a remote's cached
// release metadata when the remote source answers from its cache; without
// it such an answer fails as invalid upstream metadata (502).
// RemoteSources serves remote repositories and fetches through them; nil
// leaves the remote class without a source, so a remote repository answers
// 501.
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
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

// Protocol returns the Forge v3 protocol for the router kit, under
// /artifactory/api/puppet/{repoKey}: GET and HEAD of the release list of
// one module (v3/releases?module=), a release (v3/releases/{slug}), a
// module (v3/modules/{name}), the exact-name module search
// (v3/modules?query=) and a release's archive (v3/files/{file}). Local and
// federated repositories are served from their package index, virtual
// ones from their members in order, and remote ones through
// o.RemoteSources when it is set; a class without a source answers 501.
// The virtual class's source supports no single-item lookup, because the
// protocol reads a virtual repository's members itself.
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
	meta   puppetfmt.Metadata
	item   model.Item
	member *http.Request
	// refused marks a virtual repository's release whose archive every
	// member holding it refuses: it stays listed, and the archive route
	// does not serve it.
	refused    bool
	remoteURL  string
	revalidate func(context.Context) error
	// checkPermission rechecks only Read on the archive, for an answer
	// that lists the release; checkRead rechecks the remote's download
	// policy too, before the archive is fetched.
	checkPermission func(context.Context, string) error
	checkRead       func(context.Context, string) error
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
	if !h.k.RequireFolder(w, r, name, api.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name, listing)
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
	if !h.k.Require(w, r, puppetfmt.Path(name, version), api.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name, listing)
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
	if !h.k.RequireFolder(w, r, name, api.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name, listing)
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
	if !h.k.RequireFolder(w, r, name, api.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name, search)
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
	if !h.k.Require(w, r, path, api.ActionRead) {
		return
	}
	// The archive is a download, which Artifactory refuses at its own path
	// before it looks the release up (decision 0032; RTFACT-13710 shows the
	// Puppet service reading archives through that check): the blackout
	// 404 (rulings R9), the patterns 404 or a virtual repository's own
	// "No repository found" 404, with the policy's message rather than
	// "Puppet release not found" (R6, R10). CheckDocument rather than
	// CheckDownload, because it checks a remote repository's own policy
	// too, which the source boundary would otherwise check only once the
	// release pages had been fetched from upstream.
	if err := h.k.CheckDocument(r, path); err != nil {
		h.k.Error(w, r, err)
		return
	}
	releases, err := h.lookup(r, name, download)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, rel := range releases {
		// A version every member of a virtual repository refuses has no
		// member left to serve it, and answers as one none holds (R5).
		if rel.meta.Version == version && !rel.refused {
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
		// Read permission only: a listed release whose archive the
		// patterns reject stays listed (rulings R3), and remoteArchive
		// checks the policy when the archive itself is read.
		if rel.checkPermission != nil {
			if err := rel.checkPermission(r.Context(), rel.meta.Path); err != nil {
				return fmt.Errorf("puppet: authority: %w", pkgroute.SourceError(err, false))
			}
		}
	}
	return nil
}

// use is what a module's releases are looked up to answer, which decides
// the download policy the lookup applies.
type use int

const (
	// listing answers a release list, a release or a module record, which
	// 7.x reads from stored documents (RTFACT-26143).
	listing use = iota
	// search answers the exact-name module search, a native search route,
	// which keeps its answers under a blackout (R2b): a blacked-out local
	// repository finds nothing, and a blacked-out remote answers the
	// blackout 404 for the module folder. No pattern refuses it, since a
	// release whose archive the patterns reject stays listed (R3).
	search
	// download finds the release whose archive the archive route serves,
	// once that route has checked the archive's own path.
	download
)

// lookup returns the releases of module name the caller may read, for u.
func (h handler) lookup(r *http.Request, name string, u use) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	switch q.Repo.Class {
	case repo.Local, repo.Federated:
		// The search, a native search route, keeps its answer under a
		// blackout (R2b): it finds none of a blacked-out repository's
		// releases, an empty result rather than the blackout 404, and a
		// virtual repository's search leaves such a member out.
		if u == search {
			if generic.CheckBlackedOut(q.Repo, q.Rest) != nil {
				return nil, nil
			}
			return h.hosted(r, name)
		}
		// 7.x keeps a local repository's listings as documents
		// (RTFACT-26143) at a path no source shows, so only the blackout
		// refuses them, before anything is computed, naming the
		// request's own path (R2a, R9); patterns never do. A member of a
		// virtual repository is passed over instead (R5).
		if err := h.k.CheckBlackedOut(r, q.Rest); err != nil {
			return nil, fmt.Errorf("puppet: releases: %w", err)
		}
		return h.hosted(r, name)
	case repo.Remote:
		return h.remoteVersions(r, name, u)
	case repo.Virtual:
		return h.virtual(r, name, u)
	default:
		return nil, source.ErrUnsupported
	}
}

func (h handler) hosted(r *http.Request, name string) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	p, err := h.k.DB().Packages().Find(r.Context(), q.Repo.Key, name)
	if errors.Is(err, kernel.ErrNotFound) {
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
		// A release whose archive the include and exclude patterns reject
		// stays listed, and only the archive is refused (rulings R3): 7.x
		// writes Puppet's listings as stored documents (RTFACT-26143),
		// which list such a release as the other formats' do.
		if !h.k.Can(r, path, api.ActionRead) {
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

func (h handler) virtual(r *http.Request, name string, u use) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	byVersion := map[string]release{}
	for _, key := range q.Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("puppet: virtual member: %w", err)
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, err //nolint:wrapcheck // protocol class refusal
		}
		if !h.k.CanFolder(mr, name, api.ActionRead) {
			continue
		}
		// A blacked-out member, local or remote, and, outside the search, a
		// remote member whose patterns reject its release pages answer 404
		// here, or for the search find nothing, and are left out of the
		// merge (R5, R2b).
		found, err := h.lookup(mr, name, u)
		if err != nil {
			var response *apierror.Error
			if errors.Is(err, kernel.ErrNotFound) ||
				errors.As(err, &response) && response.Status == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		for _, v := range found {
			if !h.k.Can(r, v.meta.Path, api.ActionRead) {
				continue
			}
			// A member whose patterns reject only a release's archive
			// stays in the merge with the release listed, and the archive
			// comes from the first member that holds the version and
			// serves it (rulings R5, RTFACT-25571), so the listing gives
			// that member's checksum. The virtual repository's own
			// patterns leave the release listed too (R3); the archive
			// route refuses its archive.
			v.member = mr
			v.refused = generic.CheckDownload(member, v.meta.Path, false) != nil
			if prior, exists := byVersion[v.meta.Version]; !exists || prior.refused && !v.refused {
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
