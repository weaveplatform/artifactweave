package pubhttp

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
	pubfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

const mediaType = "application/vnd.pub.v2+json"

// Options configures the pub protocol. Blobs opens a remote's cached
// version document when the remote source answers from its cache; without
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

// Protocol returns the hosted pub protocol for the router kit, under
// /artifactory/api/pub/{repoKey}: the publish steps (GET of
// api/packages/versions/new for the upload URL, POST of the multipart
// upload there, and GET of api/packages/versions/new/complete to
// finalize), GET and HEAD of a package's versions (api/packages/{package}),
// GET of one version (api/packages/{package}/versions/{version}), and GET
// and HEAD of an archive (packages/{package}/versions/{version}.tar.gz).
// Local and federated repositories are served from their package index,
// virtual ones from their members in order, and remote ones through
// o.RemoteSources when it is set; a class without a source answers 501.
// The virtual class's source supports no single-item lookup, because the
// protocol reads a virtual repository's members itself. {repoKey}/reindex
// is reserved for JFrog's reindex operation, which the server serves
// beside the protocol.
func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name: "pub", Types: []repo.PackageType{repo.Pub}, ErrorProfile: apierror.ProfilePub,
		Sources: sources, Reserved: []string{"{repoKey}/reindex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, blobs: o.Blobs}
			r.Get("/api/packages/versions/new", h.publishStart)
			r.Post("/api/packages/versions/new", h.publishUpload)
			r.Get("/api/packages/versions/new/complete", h.publishComplete)
			r.Get("/api/packages/{package}", h.versions)
			r.Head("/api/packages/{package}", h.versions)
			r.Get("/api/packages/{package}/versions/{version}", h.version)
			r.Get("/packages/{package}/versions/{archive}", h.archive)
			r.Head("/packages/{package}/versions/{archive}", h.archive)
		},
	}
}

type release struct {
	meta       pubfmt.Metadata
	item       model.Item
	member     *http.Request
	remoteURL  string
	revalidate func(context.Context) error
	checkRead  func(context.Context, string) error
}

type versionRecord struct {
	Version       string         `json:"version"`
	ArchiveURL    string         `json:"archive_url"`
	ArchiveSHA256 string         `json:"archive_sha256,omitempty"`
	Pubspec       map[string]any `json:"pubspec"`
}

// documentPath is where 7.x keeps name's versions document, which its
// indexer writes and the versions routes read through the download path
// (Pub Repositories docs: "The index of the packages will be populated
// under the .pub, each package has its own json file"; RTDEV-85999), so a
// blacked-out repository or a pattern on that path refuses the document.
// The patterns of one release's archive do not: the release stays listed
// and only its archive is refused (R3).
func documentPath(name string) string { return ".pub/" + name + ".json" }

func packageName(r *http.Request) (string, error) {
	name, err := url.PathUnescape(chi.URLParam(r, "package"))
	if err != nil || !pubfmt.ValidName(name) {
		return "", apierror.New(http.StatusBadRequest, "invalid Dart pub package name")
	}
	return name, nil
}

func (h handler) versions(w http.ResponseWriter, r *http.Request) {
	name, err := packageName(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !h.k.RequireFolder(w, r, name, api.ActionRead) {
		return
	}
	if r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid pub versions query"))
		return
	}
	if err := h.k.CheckDocument(r, documentPath(name)); err != nil {
		h.k.Error(w, r, err)
		return
	}
	releases, err := h.lookup(r, name, false)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(releases) == 0 {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "package not found"))
		return
	}
	all := make([]versionRecord, 0, len(releases))
	for _, release := range releases {
		all = append(all, record(r, release.meta))
	}
	latest := all[0]
	for _, v := range all {
		if semver.Prerelease("v"+v.Version) == "" {
			latest = v
			break
		}
	}
	writeJSON(w, r, http.StatusOK, struct {
		Name     string          `json:"name"`
		Latest   versionRecord   `json:"latest"`
		Versions []versionRecord `json:"versions"`
	}{Name: name, Latest: latest, Versions: all})
}

func (h handler) version(w http.ResponseWriter, r *http.Request) {
	name, err := packageName(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	version, err := url.PathUnescape(chi.URLParam(r, "version"))
	if err != nil || !pubfmt.ValidVersion(version) {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Dart pub version"))
		return
	}
	if !h.k.RequireFolder(w, r, name, api.ActionRead) {
		return
	}
	if err := h.k.CheckDocument(r, documentPath(name)); err != nil {
		h.k.Error(w, r, err)
		return
	}
	releases, err := h.lookup(r, name, false)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, release := range releases {
		if release.meta.Version == version {
			writeJSON(w, r, http.StatusOK, record(r, release.meta))
			return
		}
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "version not found"))
}

func (h handler) archive(w http.ResponseWriter, r *http.Request) {
	name, err := packageName(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	archive := chi.URLParam(r, "archive")
	archive, err = url.PathUnescape(archive)
	if err != nil || !strings.HasSuffix(archive, ".tar.gz") {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Dart pub archive version"))
		return
	}
	version := strings.TrimSuffix(archive, ".tar.gz")
	if !pubfmt.ValidVersion(version) {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Dart pub archive version"))
		return
	}
	path := pubfmt.Path(name, version)
	if !h.k.Require(w, r, path, api.ActionRead) {
		return
	}
	class := pkgroute.RequestFrom(r).Repo.Class
	if class == repo.Remote || class == repo.Virtual {
		// The archive's own path, before the lookup, so the refusal names
		// it (R10): a remote's own policy, or a virtual repository's
		// patterns, which leave it no member to search ("No repository
		// found to serve the request", 6.20 VirtualRepoDownloadStrategy).
		// A local archive takes it from ServeItem.
		if err := h.k.CheckDocument(r, path); err != nil {
			h.k.Error(w, r, err)
			return
		}
	}
	if class == repo.Virtual {
		h.virtualArchive(w, r, name, version, path)
		return
	}
	releases, err := h.lookup(r, name, true)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, release := range releases {
		if release.meta.Version == version {
			h.serveArchive(w, r, release, path)
			return
		}
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "version not found"))
}

// virtualArchive serves the archive of version from the first member, in
// the virtual repository's order, that holds the version and does not
// refuse the archive: 7.x ignores a blacked-out member when it resolves
// and passes over a member that cannot serve the file, so a member whose
// policy refuses it leaves the archive to the next member that holds it
// (R5). Every member's versions are still read first, as the merge reads
// them, so a member that fails fails the download as it fails the list.
func (h handler) virtualArchive(
	w http.ResponseWriter,
	r *http.Request,
	name, version, path string,
) {
	members, err := h.memberReleases(r, name, true)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, found := range members {
		for _, release := range found {
			if release.meta.Version != version {
				continue
			}
			// archive checked the virtual repository's own patterns, so
			// a refusal here is the member's, which passes it over.
			if h.k.CheckDocument(release.member, path) != nil {
				break
			}
			h.serveArchive(w, r, release, path)
			return
		}
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "version not found"))
}

// serveArchive serves release's archive at path from the repository the
// release was listed in.
func (h handler) serveArchive(
	w http.ResponseWriter,
	r *http.Request,
	release release,
	path string,
) {
	if release.revalidate != nil {
		if err := release.revalidate(r.Context()); err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if release.remoteURL != "" {
		h.remoteArchive(w, r, release)
		return
	}
	member := r
	if release.member != nil {
		member = release.member
	}
	h.k.ServeItem(w, member, path)
}

func record(r *http.Request, meta pubfmt.Metadata) versionRecord {
	key := pkgroute.RequestFrom(r).Repo.Key
	return versionRecord{
		Version: meta.Version,
		ArchiveURL: baseurl.From(r.Context()) + "/artifactory/api/pub/" +
			url.PathEscape(key) + "/packages/" + url.PathEscape(meta.Name) +
			"/versions/" + url.PathEscape(meta.Version) + ".tar.gz",
		ArchiveSHA256: meta.SHA256, Pubspec: meta.Pubspec,
	}
}

// lookup returns name's releases in r's repository. file is set for the
// lookup an archive download makes, which the blackout alone refuses: the
// archive's own path takes the patterns, so a pattern on the versions
// document does not refuse the archive.
func (h handler) lookup(r *http.Request, name string, file bool) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	switch q.Repo.Class {
	case repo.Local, repo.Federated:
		return h.hosted(r, name)
	case repo.Remote:
		return h.remoteVersions(r, name, file)
	case repo.Virtual:
		return h.virtual(r, name, file)
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
		return nil, fmt.Errorf("pub: find package: %w", err)
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return nil, fmt.Errorf("pub: package versions: %w", err)
	}
	out := make([]release, 0, len(versions))
	for _, v := range versions {
		path := pubfmt.Path(name, v.Version)
		if !h.k.Can(r, path, api.ActionRead) {
			continue
		}
		var meta pubfmt.Metadata
		if json.Unmarshal(v.Metadata, &meta) != nil || meta.Name != name ||
			meta.Version != v.Version || meta.Path != path {
			return nil, apierror.New(http.StatusBadGateway, "invalid stored pub package index")
		}
		item, err := h.k.DB().Items().Get(r.Context(), q.Repo.Key, false, path)
		if err != nil || item.Folder || meta.SHA256 == "" || meta.SHA256 != item.Actual.SHA256 {
			return nil, apierror.New(http.StatusBadGateway, "invalid stored pub archive")
		}
		out = append(out, release{meta: meta, item: item})
	}
	sortReleases(out)
	return out, nil
}

// virtual merges the members' releases of name, an earlier member's copy
// of a version shadowing a later member's.
func (h handler) virtual(r *http.Request, name string, file bool) ([]release, error) {
	members, err := h.memberReleases(r, name, file)
	if err != nil {
		return nil, err
	}
	byVersion := map[string]release{}
	for _, found := range members {
		for _, v := range found {
			if _, exists := byVersion[v.meta.Version]; !exists {
				byVersion[v.meta.Version] = v
			}
		}
	}
	out := make([]release, 0, len(byVersion))
	for _, v := range byVersion {
		out = append(out, v)
	}
	sortReleases(out)
	return out, nil
}

// memberReleases returns, in the virtual repository's order, the releases
// of name each member the caller may read holds, each with its member's
// request. The versions document leaves out a blacked-out member, which
// 7.x ignores when it resolves, and one whose patterns reject the
// document's path ([I], R5); an archive download's lookup leaves out only
// a blacked-out member, and passes over one that refuses the archive.
func (h handler) memberReleases(r *http.Request, name string, file bool) ([][]release, error) {
	doc := documentPath(name)
	if file {
		doc = ""
	}
	members, err := h.k.MergeMembers(r, doc)
	if err != nil {
		return nil, err //nolint:wrapcheck // a refusal or a protocol class refusal, answered as it is
	}
	var out [][]release
	for _, mr := range members {
		if !h.k.CanFolder(mr, name, api.ActionRead) {
			continue
		}
		found, err := h.lookup(mr, name, file)
		if err != nil {
			var response *apierror.Error
			if errors.Is(err, kernel.ErrNotFound) ||
				errors.As(err, &response) && response.Status == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		for i := range found {
			found[i].member = mr
		}
		out = append(out, found)
	}
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
