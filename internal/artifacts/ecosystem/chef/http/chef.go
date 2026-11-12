package chefhttp

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
	cheffmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

const (
	mediaType   = "application/json"
	maxVersions = 4096
)

type handler struct{ k *pkgroute.Kit }

// Protocol returns the Chef Supermarket protocol for the router kit, under
// /artifactory/api/chef/{repoKey}/api/v1: GET and HEAD of the catalog
// (cookbooks, and search with q), a cookbook (cookbooks/{name}), a version
// (cookbooks/{name}/versions/{version}, where latest names the highest
// readable one) and its archive (.../download); POST of a cookbook, raw as
// knife-art sends it (cookbooks/{name}.tgz) or as the Supermarket multipart
// form (cookbooks); and DELETE of a version, as knife-art's unshare
// (cookbooks/{name}/{version}) or the Supermarket's, or of a whole
// cookbook. Publish and delete need a local or federated repository (405).
// Remote repositories are served through remote when it is set; without it
// a remote repository answers 501.
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
	item       model.Item
	member     *http.Request
	remoteURL  string
	revalidate func(context.Context) error
	// checkRead rechecks the caller's Read permission on a path the
	// upstream disclosed, without the download policy, so a record lists a
	// version whose archive the patterns reject; the archive's own read
	// applies the policy.
	checkRead func(context.Context, string) error
	// alternates are the later members of a virtual repository that hold
	// the same version, in order, for a download to pass on to when a
	// member's own policy refuses the archive.
	alternates []release
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
	return name, h.k.RequireFolder(w, r, name, api.ActionRead)
}

// resolution refuses the cookbook and version records a client resolves a
// cookbook from, before they are built, when the repository is blacked
// out: 7.x ignores a blacked-out repository when it resolves (R2a). It
// names the request's own path, since 7.x documents none it keeps this
// metadata at, and so the patterns do not refuse it (#216). The catalog
// and search keep their answers under the blackout, as a native search
// does (R2b), and a virtual repository has no blackout.
func (h handler) resolution(w http.ResponseWriter, r *http.Request) bool {
	if err := h.k.CheckBlackedOut(r, requestPath(r)); err != nil {
		h.k.Error(w, r, err)
		return false
	}
	return true
}

// requestPath is the request's path after the repository key. The kit has
// parsed it before any route runs.
func requestPath(r *http.Request) string {
	p, _ := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	return p.String()
}

// archivePolicy applies the download policy to the archive at path, so a
// refused download answers the policy's 404 and message rather than "Chef
// cookbook or version not found" (decision 0032, RTDEV-47144): a hosted
// repository's blackout and patterns, and a virtual repository's own
// patterns with "No repository found to serve the request". A remote's
// blackout is checked here, before its upstream is asked where the
// archive is; its patterns are checked when the source boundary reads the
// archive.
func (h handler) archivePolicy(w http.ResponseWriter, r *http.Request, path string) bool {
	var err error
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		err = h.k.CheckBlackedOut(r, path)
	} else {
		err = h.k.CheckDownload(r, path)
	}
	if err != nil {
		h.k.Error(w, r, err)
		return false
	}
	return true
}

func (h handler) cookbook(w http.ResponseWriter, r *http.Request) {
	name, ok := h.identity(w, r)
	if !ok || !h.resolution(w, r) {
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

// resolve finds the version a version record or a download names. A
// download of a named version is refused at its archive's path before the
// version is looked up, so the policy answers whether the version exists
// or not; latest is resolved first, as a record is, and its archive
// checked once it is known.
func (h handler) resolve(w http.ResponseWriter, r *http.Request, download bool) (release, bool) {
	name, ok := h.identity(w, r)
	if !ok {
		return release{}, false
	}
	version := strings.ReplaceAll(chi.URLParam(r, "version"), "_", ".")
	if version != "latest" && !cheffmt.ValidVersion(version) {
		h.k.Error(w, r, apierror.New(400, "invalid cookbook version"))
		return release{}, false
	}
	if version != "latest" && !h.k.Require(w, r, cheffmt.Path(name, version), api.ActionRead) {
		return release{}, false
	}
	if download && version != "latest" {
		if !h.archivePolicy(w, r, cheffmt.Path(name, version)) {
			return release{}, false
		}
	} else if !h.resolution(w, r) {
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
			if download && version == "latest" && !h.archivePolicy(w, r, rel.meta.Path) {
				return release{}, false
			}
			return rel, true
		}
	}
	h.k.Error(w, r, missing())
	return release{}, false
}

func (h handler) version(w http.ResponseWriter, r *http.Request) {
	rel, ok := h.resolve(w, r, false)
	if ok {
		writeJSON(w, r, 200, versionRecord(r, rel.meta))
	}
}

// archive serves a version's archive. A virtual repository's comes from
// the first member, in order, that holds the version and whose own policy
// accepts the archive: a member that refuses it is passed over, as one
// without it is (R5, decision 0032), and when none serves it the answer
// is Chef's not-found.
func (h handler) archive(w http.ResponseWriter, r *http.Request) {
	rel, ok := h.resolve(w, r, true)
	if !ok {
		return
	}
	for _, candidate := range append([]release{rel}, rel.alternates...) {
		if candidate.member != nil && h.memberRefuses(candidate.member, candidate.meta.Path) {
			continue
		}
		h.serveArchive(w, r, candidate)
		return
	}
	h.k.Error(w, r, missing())
}

func (h handler) serveArchive(w http.ResponseWriter, r *http.Request, rel release) {
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

// memberRefuses reports whether a virtual repository's member refuses the
// archive at path by its own download policy; resolve has checked the
// virtual repository's own patterns, which the member request carries. A
// remote member's is checked here, before its upstream is asked, at the
// path the source boundary checks when it reads the archive.
func (h handler) memberRefuses(mr *http.Request, path string) bool {
	member := pkgroute.RequestFrom(mr).Repo
	if member.Class == repo.Remote {
		return generic.CheckDownload(member, path, false) != nil
	}
	return h.k.CheckDownload(mr, path) != nil
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
	if !h.k.RequireFolder(w, r, name, api.ActionWrite) {
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
		kernel.Checksums{},
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
	if !h.k.RequireFolder(w, r, "", api.ActionRead) {
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
			!h.k.CanFolder(r, name, api.ActionRead) {
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
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("chef: member: %w", err)
			}
			// 7.x ignores a blacked-out member when it resolves (R5), so
			// a blacked-out remote member's refused catalog does not
			// refuse the virtual repository's.
			if generic.CheckBlackedOut(member, "") != nil {
				continue
			}
			mr, err := h.k.MemberRequest(r, member)
			if err != nil {
				return nil, fmt.Errorf("chef: member request: %w", err)
			}
			if !h.k.CanFolder(mr, "", api.ActionRead) {
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
	page := kernel.Page{Limit: kernel.MaxLimit}
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
	if errors.Is(err, kernel.ErrNotFound) {
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
		// Read permission decides what is listed: a version whose archive
		// the patterns reject stays listed, and only its download is
		// refused (R3; RTFACT-20145 shows an excluded cookbook still
		// found by search). A blacked-out repository lists nothing, which
		// is what its catalog and search answer (R2b); its records and
		// downloads refuse before they look (resolution, archivePolicy).
		if !h.k.Can(r, path, api.ActionRead) || generic.CheckBlackedOut(q.Repo, path) != nil {
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
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("chef: virtual member: %w", err)
		}
		// 7.x ignores a blacked-out member when it resolves (R5).
		if generic.CheckBlackedOut(member, "") != nil {
			continue
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, err //nolint:wrapcheck // protocol class refusal
		}
		if !h.k.CanFolder(mr, name, api.ActionRead) {
			continue
		}
		found, err := h.lookup(mr, name)
		if err != nil {
			var response *apierror.Error
			if errors.Is(err, kernel.ErrNotFound) ||
				errors.As(err, &response) && response.Status == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		// A version whose archive a member's patterns or the virtual
		// repository's own reject stays listed (R3, R5); its download
		// passes over that member or is refused (archive).
		for _, v := range found {
			if !h.k.Can(r, v.meta.Path, api.ActionRead) {
				continue
			}
			v.member = mr
			if first, exists := byVersion[v.meta.Version]; exists {
				first.alternates = append(first.alternates, v)
				byVersion[v.meta.Version] = first
			} else {
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
