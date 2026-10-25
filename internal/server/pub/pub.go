// Package pub serves the hosted Dart pub protocol through Artifactory's
// /artifactory/api/pub/{repo} URL.
package pub

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

	pubfmt "github.com/weaveplatform/artifactweave/internal/format/pub"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const mediaType = "application/vnd.pub.v2+json"

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
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name: "pub", Types: []repo.PackageType{repo.Pub}, Surface: apierror.Pub,
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
	item       types.Item
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
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	if r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid pub versions query"))
		return
	}
	releases, err := h.lookup(r, name)
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
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
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
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, release := range releases {
		if release.meta.Version != version {
			continue
		}
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
		return
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "version not found"))
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
		return nil, fmt.Errorf("pub: find package: %w", err)
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return nil, fmt.Errorf("pub: package versions: %w", err)
	}
	out := make([]release, 0, len(versions))
	for _, v := range versions {
		path := pubfmt.Path(name, v.Version)
		if !h.k.Can(r, path, types.ActionRead) {
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

func (h handler) virtual(r *http.Request, name string) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	byVersion := map[string]release{}
	for _, key := range q.Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("pub: virtual member: %w", err)
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
			if _, exists := byVersion[v.meta.Version]; !exists {
				v.member = mr
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
