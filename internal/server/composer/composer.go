// Package composer serves Composer 1 local catalogs and Composer 2 metadata
// through the native Artifactory Composer URL.
package composer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/format/composer"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

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
		Name:         "composer",
		Types:        []repo.PackageType{repo.Composer},
		ErrorProfile: apierror.ProfileArtifactory,
		Resolve:      resolveKey,
		Sources:      sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k, o.Blobs}
			r.Get("/packages.json", h.root)
			r.Head("/packages.json", h.root)
			r.Get("/p2/*", h.p2)
			r.Head("/p2/*", h.p2)
			r.Get("/dist/*", h.dist)
			r.Head("/dist/*", h.dist)
			r.Post("/reindex", h.reindex)
		},
	}
}

func resolveKey(rel string) []pkgroute.Key {
	keys := pkgroute.FirstSegment(rel)
	if len(keys) == 0 {
		return nil
	}
	key, cached := types.ResolveRepoKey(keys[0].Key)
	keys[0].Key, keys[0].CacheOnly = key, cached
	return keys
}

type release struct {
	Name, Version, Path string
	Document            map[string]any
	Item                types.Item
	RemoteURL, SHA1     string
	Member              *http.Request
	revalidate          func(context.Context) error
}
type catalog map[string][]release

func (h handler) root(w http.ResponseWriter, r *http.Request) {
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	if r.URL.RawQuery != "" {
		h.bad(w, r)
		return
	}
	q := pkgroute.RequestFrom(r)
	if q.Repo.Class == repo.Remote {
		if _, _, err := h.remoteRoot(r); err != nil {
			h.k.Error(w, r, err)
			return
		}
		respondJSON(
			w,
			r,
			map[string]any{
				"packages": map[string]any{},
				"metadata-url": "/artifactory/api/composer/" + url.PathEscape(
					publicKey(r),
				) + "/p2/%package%.json",
			},
		)
		return
	}
	if q.Repo.Class == repo.Virtual {
		// V2 discovers each package by p2; the local v1 catalog is built only
		// for hosted repositories while v1 virtual is outside JFrog support.
		respondJSON(
			w,
			r,
			map[string]any{
				"packages": map[string]any{},
				"metadata-url": "/artifactory/api/composer/" + url.PathEscape(
					publicKey(r),
				) + "/p2/%package%.json",
			},
		)
		return
	}
	cat, err := h.hosted(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	// Composer 1 consumes the complete packages map. Composer 2 treats a
	// nonempty map together with metadata-url as provider data and expects
	// legacy uid fields, so give it the p2 discovery document alone.
	if strings.HasPrefix(r.UserAgent(), "Composer/1.") {
		all := map[string]map[string]map[string]any{}
		for name, releases := range cat {
			for _, rel := range releases {
				if all[name] == nil {
					all[name] = map[string]map[string]any{}
				}
				all[name][rel.Version] = rel.Document
			}
		}
		respondJSON(w, r, map[string]any{"packages": all})
		return
	}
	respondJSON(
		w,
		r,
		map[string]any{
			"packages": map[string]any{},
			"metadata-url": "/artifactory/api/composer/" + url.PathEscape(
				publicKey(r),
			) + "/p2/%package%.json",
		},
	)
}

func (h handler) p2(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		h.bad(w, r)
		return
	}
	name, dev, ok := parseP2(pkgroute.RequestFrom(r).Rest)
	if !ok {
		h.notFound(w, r)
		return
	}
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.notFound(w, r)
			return
		}
		h.k.Error(w, r, err)
		return
	}
	entries := []map[string]any{}
	for _, rel := range releases {
		isDev := strings.HasPrefix(rel.Version, "dev-") || strings.HasSuffix(rel.Version, "-dev")
		if isDev == dev {
			entries = append(entries, rel.Document)
		}
	}
	if len(entries) == 0 {
		h.notFound(w, r)
		return
	}
	for _, rel := range releases {
		if rel.revalidate != nil {
			if err := rel.revalidate(r.Context()); err != nil {
				h.k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
		}
	}
	respondJSON(w, r, map[string]any{"packages": map[string]any{name: entries}})
}

func parseP2(rest string) (string, bool, bool) {
	if !strings.HasPrefix(rest, "p2/") || !strings.HasSuffix(rest, ".json") {
		return "", false, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(rest, "p2/"), ".json")
	name, err := url.PathUnescape(value)
	if err != nil || strings.Contains(name, "%") || strings.Contains(name, "//") {
		return "", false, false
	}
	dev := strings.HasSuffix(name, "~dev")
	name = strings.TrimSuffix(name, "~dev")
	return name, dev, composer.ValidName(name)
}

func (h handler) lookup(r *http.Request, name string) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	switch q.Repo.Class {
	case repo.Local, repo.Federated:
		cat, err := h.hosted(r)
		return cat[name], err
	case repo.Remote:
		return h.remoteP2(r, name)
	case repo.Virtual:
		members, err := h.members(r)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			if !h.k.CanFolder(member, name, types.ActionRead) {
				continue
			}
			found, err := h.lookup(member, name)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if len(found) > 0 {
				for i := range found {
					found[i].Member = member
					found[i].Document["dist"] = distDocument(
						r,
						name,
						found[i].Version,
						found[i].SHA1,
					)
				}
				return found, nil
			}
		}
		return nil, store.ErrNotFound
	}
	return nil, source.ErrUnsupported
}

func (h handler) members(r *http.Request) ([]*http.Request, error) {
	q := pkgroute.RequestFrom(r)
	seen := map[string]bool{q.Repo.Key: true}
	var members []*http.Request
	for _, key := range q.Repo.Repositories {
		if seen[key] {
			return nil, apierror.New(http.StatusBadRequest, "invalid Composer virtual member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get composer virtual member %s: %w", key, err)
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("composer member request %s: %w", key, err)
		}
		members = append(members, mr)
	}
	return members, nil
}

func (h handler) hosted(r *http.Request) (catalog, error) {
	if h.blobs == nil {
		return nil, apierror.ErrNotImplemented
	}
	q := pkgroute.RequestFrom(r)
	cat := catalog{}
	items, cursor, err := h.k.DB().
		Items().
		Descendants(r.Context(), q.Repo.Key, false, "", store.Page{Limit: 500})
	for err == nil {
		for _, it := range items {
			if it.Folder || !strings.HasSuffix(it.FullPath(), ".zip") {
				continue
			}
			if !h.k.Can(r, it.FullPath(), types.ActionRead) {
				continue
			}
			props, e := h.k.DB().Items().Properties(r.Context(), it.ID)
			if e != nil {
				return nil, fmt.Errorf("composer item properties %s: %w", it.FullPath(), e)
			}
			version := ""
			if len(props["composer.version"]) == 1 {
				version = props["composer.version"][0]
			}
			body, info, e := h.blobs.Open(r.Context(), it.Actual.SHA256)
			if e != nil {
				return nil, fmt.Errorf("open composer archive %s: %w", it.FullPath(), e)
			}
			pkg, e := composer.ParseArchive(r.Context(), body, info.Size, version)
			_ = body.Close()
			if e != nil || !composer.HostedPath(it.FullPath(), pkg.Name) {
				return nil, apierror.New(
					http.StatusBadGateway,
					"stored Composer archive is invalid",
				)
			}
			if !h.k.CanFolder(r, pkg.Name, types.ActionRead) {
				continue
			}
			doc := pkg.Document()
			doc["dist"] = distDocument(r, pkg.Name, pkg.Version, it.Actual.SHA1)
			cat[pkg.Name] = append(
				cat[pkg.Name],
				release{
					Name:     pkg.Name,
					Version:  pkg.Version,
					Path:     it.FullPath(),
					Document: doc,
					Item:     it,
					SHA1:     it.Actual.SHA1,
				},
			)
		}
		if cursor == "" {
			break
		}
		items, cursor, err = h.k.DB().
			Items().
			Descendants(r.Context(), q.Repo.Key, false, "", store.Page{Limit: 500, After: cursor})
	}
	if err != nil {
		return nil, fmt.Errorf("list composer items: %w", err)
	}
	for name := range cat {
		sort.Slice(
			cat[name],
			func(i, j int) bool { return cat[name][i].Version > cat[name][j].Version },
		)
	}
	return cat, nil
}

func distDocument(r *http.Request, name, version, sha1 string) map[string]any {
	return map[string]any{
		"type": "zip",
		"url": fmt.Sprintf(
			"%s/artifactory/api/composer/%s/dist/%s/%s.zip",
			baseurl.From(r.Context()),
			url.PathEscape(publicKey(r)),
			name,
			url.PathEscape(version),
		),
		"shasum": sha1,
	}
}

func publicKey(r *http.Request) string {
	q := pkgroute.RequestFrom(r)
	if q.CacheOnly {
		return q.Repo.Key + types.CacheSuffix
	}
	return q.Repo.Key
}

func (h handler) dist(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		h.bad(w, r)
		return
	}
	name, version, ok := parseDist(pkgroute.RequestFrom(r).Rest)
	if !ok {
		h.notFound(w, r)
		return
	}
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.notFound(w, r)
			return
		}
		h.k.Error(w, r, err)
		return
	}
	for _, rel := range releases {
		if rel.Version != version {
			continue
		}
		if rel.revalidate != nil {
			if err := rel.revalidate(r.Context()); err != nil {
				h.k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
		}
		q := pkgroute.RequestFrom(r)
		if q.Repo.Class == repo.Virtual {
			if rel.Member == nil {
				break
			}
			r = rel.Member
		}
		if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
			h.remoteDist(w, r, rel)
			return
		}
		if !h.k.Can(r, rel.Path, types.ActionRead) {
			h.k.Error(w, r, apierror.New(http.StatusForbidden, "Composer archive read denied"))
			return
		}
		h.k.ServeFound(w, r, pkgroute.Found{Repo: pkgroute.RequestFrom(r).Repo, Item: rel.Item})
		return
	}
	h.notFound(w, r)
}

func parseDist(rest string) (string, string, bool) {
	if !strings.HasPrefix(rest, "dist/") || !strings.HasSuffix(rest, ".zip") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(rest, "dist/"), ".zip"), "/")
	if len(parts) != 3 {
		return "", "", false
	}
	for i := range parts {
		value, err := url.PathUnescape(parts[i])
		if err != nil || strings.Contains(value, "/") || strings.Contains(value, "%") {
			return "", "", false
		}
		parts[i] = value
	}
	name := parts[0] + "/" + parts[1]
	return name, parts[2], composer.ValidName(name) && composer.ValidVersion(parts[2])
}

func (h handler) reindex(w http.ResponseWriter, r *http.Request) {
	q := pkgroute.RequestFrom(r)
	if q.Repo.Class != repo.Local && q.Repo.Class != repo.Federated {
		h.k.Error(
			w,
			r,
			apierror.New(http.StatusBadRequest, "Composer reindex requires hosted repository"),
		)
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionManage) {
		return
	}
	if _, err := h.hosted(r); err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "Composer repository metadata verified.\n")
}

func (h handler) bad(w http.ResponseWriter, r *http.Request) {
	h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Composer request"))
}

func (h handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "Composer package not found"))
}

func respondJSON(w http.ResponseWriter, r *http.Request, v any) {
	data, _ := json.Marshal(v)
	digest := sha256.Sum256(data)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, digest))
	w.Header().Set("Last-Modified", time.Now().UTC().Truncate(time.Second).Format(http.TimeFormat))
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}
