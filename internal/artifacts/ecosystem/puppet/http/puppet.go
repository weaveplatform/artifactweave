package puppethttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
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
// (v3/modules?query=), a release's archive (v3/files/{file}), and the
// lists of every release and module a local or federated repository
// hosts (v3/releases and v3/modules with no query but a page). Local and
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
	meta puppetfmt.Metadata
	item model.Item
	// details are the record's fields that the archive's metadata does not
	// give: a hosted release's from its archive item, a remote's as its
	// upstream gave them.
	details details
	member  *http.Request
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
	if pagingOnly(query) {
		h.listReleases(w, r, query)
		return
	}
	name := query.Get("module")
	if !puppetfmt.ValidName(name) ||
		query.Get("sort_by") != "" && query.Get("sort_by") != "version" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet module"))
		return
	}
	// The list pages as the whole-repository lists do: 20 a page unless
	// limit asks for up to 100 (parsePage), as RTFACT-14557 quotes 7.x
	// answering a release list and as the Forge pages. An offset beyond the
	// most releases a module may list keeps its 400.
	limit, offset, err := parsePage(query)
	if err == nil && offset > maxVersions {
		err = apierror.New(http.StatusBadRequest, "invalid Puppet offset")
	}
	if err != nil {
		h.k.Error(w, r, err)
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
	start, end := window(offset, limit, len(releases))
	out := make([]releaseRecord, 0, end-start)
	for _, rel := range releases[start:end] {
		out = append(out, record(rel))
	}
	writeJSON(w, r, http.StatusOK, struct {
		Pagination listPage        `json:"pagination"`
		Results    []releaseRecord `json:"results"`
	}{newListPage("/v3/releases", "module="+name, limit, offset, len(releases)), out})
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
			writeJSON(w, r, 200, record(rel))
			return
		}
	}
	h.k.Error(w, r, apierror.New(404, "Puppet release not found"))
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
	writeJSON(w, r, 200, newModuleRecord(releases))
}

// modules answers v3/modules: the whole-repository list when the query
// names nothing but a page, else the module search. The search is
// deliberately exact-name and bounded, and does not proxy arbitrary
// queries: it answers the one module named, paged as the lists are. 7.x
// pages it too, since its own virtual repository asks a member's upstream
// for "v3/modules?query=...&limit=100" (RTFACT-13687), and the Forge pages
// its search the same way, keeping query in each link.
func (h handler) modules(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil && pagingOnly(q) {
		h.listModules(w, r, q)
		return
	}
	if err != nil || !searchQuery(q) {
		h.k.Error(w, r, apierror.New(400, "search requires an exact Puppet module name"))
		return
	}
	limit, offset, err := parsePage(q)
	if err != nil {
		h.k.Error(w, r, err)
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
	found := []moduleRecord{}
	if len(releases) > 0 {
		found = append(found, newModuleRecord(releases))
	}
	start, end := window(offset, limit, len(found))
	writeJSON(w, r, http.StatusOK, struct {
		Pagination listPage       `json:"pagination"`
		Results    []moduleRecord `json:"results"`
	}{newListPage("/v3/modules", "query="+name, limit, offset, len(found)), found[start:end]})
}

// searchQuery reports whether q is a module search: one valid module name
// as query, and at most one limit and one offset.
func searchQuery(q url.Values) bool {
	for key, values := range q {
		if len(values) != 1 || key != "query" && key != "limit" && key != "offset" {
			return false
		}
	}
	return puppetfmt.ValidName(q.Get("query"))
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
	// 404 (R9), the patterns 404 or a virtual repository's own
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
		// patterns reject stays listed (R3), and remoteArchive
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
		// A release whose archive the include and exclude patterns reject
		// stays listed, and only the archive is refused (R3): 7.x
		// writes Puppet's listings as stored documents (RTFACT-26143),
		// which list such a release as the other formats' do.
		if !h.k.Can(r, puppetfmt.Path(name, v.Version), api.ActionRead) {
			continue
		}
		rel, err := h.stored(r, name, v)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	sortReleases(out)
	return out, nil
}

// stored returns the release of module name that the package index's
// version v records, once the index entry and the stored archive agree; a
// release whose entry or archive does not match fails with 502 rather than
// being advertised.
func (h handler) stored(r *http.Request, name string, v model.PackageVersion) (release, error) {
	path := puppetfmt.Path(name, v.Version)
	var meta puppetfmt.Metadata
	if json.Unmarshal(v.Metadata, &meta) != nil || meta.Name != name ||
		meta.Version != v.Version || meta.Path != path {
		return release{}, apierror.New(http.StatusBadGateway, "invalid stored Puppet package index")
	}
	key := pkgroute.RequestFrom(r).Repo.Key
	item, err := h.k.DB().Items().Get(r.Context(), key, false, path)
	if err != nil || item.Folder || meta.SHA256 == "" || meta.SHA256 != item.Actual.SHA256 {
		return release{}, apierror.New(http.StatusBadGateway, "invalid stored Puppet archive")
	}
	stats, err := h.k.DB().Items().Stats(r.Context(), item.ID)
	if err != nil {
		return release{}, fmt.Errorf("puppet: statistics of %s/%s: %w", key, path, err)
	}
	return release{meta: meta, item: item, details: hostedDetails(item, stats)}, nil
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
			// serves it (R5, RTFACT-25571), so the listing gives
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
	slices.SortFunc(
		out,
		func(a, b release) int { return newestFirst(a.meta.Version, b.meta.Version) },
	)
}

// newestFirst orders Puppet versions from the highest semantic version
// down, the order every release list answers in.
func newestFirst(a, b string) int {
	if cmp := semver.Compare("v"+a, "v"+b); cmp != 0 {
		return -cmp
	}
	return strings.Compare(a, b)
}

// writeJSON answers doc as JSON, writing "&", "<" and ">" as they are
// rather than as &, < and >, as 7.x writes them: in its
// links (RTFACT-14557) and in a release's dependency requirements, such as
// ">= 4.13.1 < 6.0.0" (RTFACT-19130). The JSON is the same either way; the
// bytes are 7.x's.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, doc any) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(doc)
	}
}
