package chefhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
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
	return name, h.requireRead(w, r, name, true)
}

// requireRead refuses a caller who may not read path, a folder when folder
// is set, in the request's repository before anything is looked up. A
// virtual repository is not asked: no permission target can name one
// (decision 0006), so each member decides Read on what it serves and a
// member the caller may not read is not found. A caller who may read only
// the members is served, and one who may read none gets Chef's not-found
// (decision 0008, R3).
func (h handler) requireRead(w http.ResponseWriter, r *http.Request, path string, folder bool) bool {
	switch {
	case pkgroute.RequestFrom(r).Repo.Class == repo.Virtual:
		return true
	case folder:
		return h.k.RequireFolder(w, r, path, api.ActionRead)
	default:
		return h.k.Require(w, r, path, api.ActionRead)
	}
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
	h.record(w, r, "cookbooks/"+name, func(r *http.Request) (any, error) {
		found, err := h.lookup(r, name)
		if err == nil && len(found) == 0 {
			err = missing()
		}
		if err == nil {
			err = h.authority(r, found)
		}
		if err != nil {
			return nil, err
		}
		return cookbookRecord(r, found), nil
	})
}

// record answers the cookbook or version record build makes. Through a
// virtual repository the record is kept for the virtual repository's
// virtualRetrievalCachePeriodSecs (pkgroute.Kit.Merged, decision 0008 R8):
// 7.161.26 takes the period for a Chef virtual repository in its REST
// model (VirtualRepositoryConfigurationImpl 91-93) and its UI
// (RepoConfigDescriptorBuilder.populateVirtualChefDescriptor 944-950),
// and VirtualRepo.isCachedResourceExpired reads it. A member's new version
// shows once the record expires; a rebuild that finds the cookbook or
// version in no member answers Chef's not-found and drops the kept record,
// as 7.x drops a cached resource no member serves any more
// (ArtifactoryVirtualDownloadStrategy.getInfoInternal 90-136). Which of
// Chef's documents the closed handler keeps is [I]: the cookbook and
// version records are the ones merged from the members. A remote member's
// authority is checked when the record is built, not on a hit, as 7.x
// serves a kept document without asking its members. document names the
// record.
func (h handler) record(
	w http.ResponseWriter,
	r *http.Request,
	document string,
	build func(*http.Request) (any, error),
) {
	e, err := h.k.Merged(r, "", document, func(r *http.Request) (virtual.Entry, error) {
		doc, err := build(r)
		if err != nil {
			return virtual.Entry{}, err
		}
		var body bytes.Buffer
		err = json.NewEncoder(&body).Encode(doc)
		return virtual.Entry{Body: body.Bytes()}, err //nolint:wrapcheck // a record of strings and maps always encodes
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeBody(w, r, 200, e.Body)
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
	if version != "latest" && !h.requireRead(w, r, cheffmt.Path(name, version), false) {
		return release{}, false
	}
	if download && version != "latest" {
		if !h.archivePolicy(w, r, cheffmt.Path(name, version)) {
			return release{}, false
		}
	} else if !h.resolution(w, r) {
		return release{}, false
	}
	if !download {
		// version answers the record from find itself, through the cache.
		return release{meta: cheffmt.Metadata{Name: name, Version: version}}, true
	}
	rel, err := h.find(r, name, version)
	if err != nil {
		h.k.Error(w, r, err)
		return release{}, false
	}
	if version == "latest" && !h.archivePolicy(w, r, rel.meta.Path) {
		return release{}, false
	}
	return rel, true
}

// find returns version of cookbook name, or its highest readable version
// for latest, once its authority holds, or Chef's not-found.
func (h handler) find(r *http.Request, name, version string) (release, error) {
	found, err := h.lookup(r, name)
	if err != nil {
		return release{}, err
	}
	for _, rel := range found {
		if version == "latest" || rel.meta.Version == version {
			if err = h.authority(r, []release{rel}); err != nil {
				return release{}, err
			}
			return rel, nil
		}
	}
	return release{}, missing()
}

func (h handler) version(w http.ResponseWriter, r *http.Request) {
	want, ok := h.resolve(w, r, false)
	if !ok {
		return
	}
	name, version := want.meta.Name, want.meta.Version
	h.record(w, r, "cookbooks/"+name+"/versions/"+version, func(r *http.Request) (any, error) {
		rel, err := h.find(r, name, version)
		if err != nil {
			return nil, err
		}
		return versionRecord(r, rel.meta), nil
	})
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

// publishTarget returns the request a share to r's repository stores with:
// r itself for a local or federated repository, or, for a virtual
// repository, a member request for its default deployment repository, a
// local or federated repository (virtual.DeployTarget). 7.x deploys through
// a virtual repository to that repository and answers 405 with Allow: GET
// without one, before it checks any permission
// (UploadServiceUtils.getTargetRepository 248-261, 7.161.26; decision 0008,
// R6). That knife's share through a virtual repository takes the same path
// is [I]: the Chef handler is closed code. A remote repository takes no
// share.
func (h handler) publishTarget(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	var err error
	target := r
	switch v := pkgroute.RequestFrom(r).Repo; v.Class {
	case repo.Local, repo.Federated:
	case repo.Virtual:
		var c repo.Config
		c, err = virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
		switch {
		case errors.Is(err, virtual.ErrNoDeployTarget):
			err = itemkit.NoDeployTarget(v.Key)
		case err == nil:
			target, err = h.k.MemberRequest(r, c)
		default:
			err = fmt.Errorf("chef: %w", err)
		}
	default:
		err = apierror.New(405, "cookbook publish requires a local or federated repository")
	}
	if err != nil {
		h.k.Error(w, r, err)
		return nil, false
	}
	return target, true
}

// publish stores a shared cookbook in the repository publishTarget names,
// on whose cookbook folder the caller needs Write. The record it answers
// names the request's own repository, so a share through a virtual
// repository points the client back at it.
func (h handler) publish(w http.ResponseWriter, r *http.Request) {
	target, ok := h.publishTarget(w, r)
	if !ok {
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
	if !h.k.RequireFolder(w, target, name, api.ActionWrite) {
		return
	}
	target.Body = http.MaxBytesReader(w, r.Body, 2*cheffmt.MaxArchiveBytes)
	var upload pkgroute.Form
	if name == "" {
		upload, err = h.k.ReadFormLimited(target, "tarball", cheffmt.MaxArchiveBytes)
	} else {
		upload.File, err = h.k.Upload(target, target.Body)
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
		target,
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
		return h.virtual(r, name, func(mr *http.Request) ([]release, error) {
			return h.lookup(mr, name)
		})
	default:
		return nil, source.ErrUnsupported
	}
}

// hosted returns the versions of name that the repository's package index
// holds and the caller may read: a local or federated repository's own,
// or a remote's cache, whose archives the index links in the cache
// (pkgindex.Stored), which a remote's search lists (catalog).
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
		if !h.listable(r, name, v.Version) {
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

// listable reports whether hosted lists version of cookbook name. Read
// permission decides what is listed: a version whose archive the patterns
// reject stays listed, and only its download is refused (R3; RTFACT-20145
// shows an excluded cookbook still found by search). A blacked-out
// repository lists nothing, which is what its catalog and search answer
// (R2b); its records and downloads refuse before they look (resolution,
// archivePolicy).
func (h handler) listable(r *http.Request, name, version string) bool {
	path := cheffmt.Path(name, version)
	return h.k.Can(r, path, api.ActionRead) &&
		generic.CheckBlackedOut(pkgroute.RequestFrom(r).Repo, path) == nil
}

// stored returns the release of cookbook name that the package index's
// version v records, once the index entry and the stored archive agree; a
// release whose entry or archive does not match fails with 502 rather than
// being listed.
func (h handler) stored(r *http.Request, name string, v model.PackageVersion) (release, error) {
	q := pkgroute.RequestFrom(r)
	path := cheffmt.Path(name, v.Version)
	var meta cheffmt.Metadata
	if json.Unmarshal(v.Metadata, &meta) != nil || meta.Name != name ||
		meta.Version != v.Version || meta.Path != path {
		return release{}, apierror.New(http.StatusBadGateway, "invalid stored Chef package index")
	}
	item, err := h.k.DB().Items().Get(
		r.Context(), q.Repo.Key, pkgindex.Stored(q.Repo).Cached, path,
	)
	if err != nil || item.Folder || meta.SHA256 == "" || meta.SHA256 != item.Actual.SHA256 {
		return release{}, apierror.New(http.StatusBadGateway, "invalid stored Chef archive")
	}
	return release{meta: meta, item: item}, nil
}

// virtual merges the versions of name that find returns for each member
// of a virtual repository (cheffmt.Releases), in the package handlers' walk
// (pkgroute.Collect): nested virtual repositories flattened with a cycle or
// a duplicate skipped (decision 0008, R2), and a member that is blacked
// out, that the caller may read nothing in, or whose read misses or is
// refused passed over (R3, R5). A member's fault, an upstream's 5xx, one it
// cannot reach or an answer that fails its checks, is passed over too, with
// a warning in the log (pkgroute.Kit.WarnPassedOver) [I]; a member's
// "Chef releases exceed limit" still fails the merge. A version is the
// first member's that lists it, with Read asked in that member alone: no
// permission target can name the virtual repository (decision 0006), so a
// caller who may read only the members is served. A version whose archive a
// member's patterns or the virtual repository's own reject stays listed
// (R3, R5); its download passes over that member or is refused (archive).
func (h handler) virtual(
	r *http.Request,
	name string,
	find func(*http.Request) ([]release, error),
) ([]release, error) {
	parts, err := pkgroute.Collect(h.k, r, "", func(mr *http.Request) ([]release, error) {
		var found []release
		var err error
		if h.k.CanFolder(mr, name, api.ActionRead) {
			found, err = find(mr)
		}
		err = pkgroute.SourceError(err, false)
		h.k.WarnPassedOver(mr, err)
		if err != nil {
			return nil, err //nolint:wrapcheck // the member's answer, as the protocol gives it
		}
		for i := range found {
			found[i].member = mr
		}
		return found, nil
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the kit's answer or a member's fault, as it is
	}
	out, _ := cheffmt.Releases[release]{
		Version: func(rel release) string { return rel.meta.Version },
		Also: func(first, later release) release {
			first.alternates = append(first.alternates, later)
			return first
		},
	}.Merge(r.Context(), parts)
	if len(out) > maxVersions {
		return nil, apierror.New(503, "Chef releases exceed limit")
	}
	return out, nil
}

func sortReleases(out []release) {
	slices.SortFunc(
		out,
		func(a, b release) int { return newestFirst(a.meta.Version, b.meta.Version) },
	)
}

// newestFirst orders Chef versions from the highest semantic version down,
// the order a cookbook's versions are listed in: the reverse of the
// package index's (model.CompareForge), so a list may read it from the
// index (#1426).
func newestFirst(a, b string) int { return model.CompareForge(b, a) }

func writeJSON(w http.ResponseWriter, r *http.Request, status int, doc any) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(doc)
	}
}

// writeBody answers body, a document writeJSON's encoder wrote, as
// writeJSON answers it.
func writeBody(w http.ResponseWriter, r *http.Request, status int, body []byte) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
