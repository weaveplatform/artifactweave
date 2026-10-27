// Package cocoapods serves the current CocoaPods CDN protocol through
// /artifactory/api/pods/{repoKey}. The retired Git specs plugin is not a
// second protocol surface.
package cocoapods

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type Options struct{ RemoteSources *source.Service }

func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name: "pods", Types: []repo.PackageType{repo.CocoaPods},
		ErrorProfile: apierror.ProfileArtifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
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

type handler struct{ k *pkgroute.Kit }

type version struct {
	archive podfmt.Archive
	request *http.Request
}

func notFound() error { return apierror.New(http.StatusNotFound, "CocoaPods resource not found") }

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil || p.Folder || len(p.Props) != 0 || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid CocoaPods CDN path"))
		return
	}
	path := p.String()
	if path == "" || strings.Contains(path, "//") {
		h.k.Error(w, r, notFound())
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.remoteRead(w, r, path)
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		h.virtualRead(w, r, path)
		return
	}
	if strings.HasPrefix(path, "packages/") {
		h.localArchive(w, r, strings.TrimPrefix(path, "packages/"))
		return
	}
	if path == "CocoaPods-version.yml" {
		serveBytes(
			w,
			r,
			"application/x-yaml",
			[]byte("---\nmin: 1.0.0\nlast: 1.17.0\nprefix_lengths:\n- 1\n- 1\n- 1\n"),
		)
		return
	}
	if path == "deprecated_podspecs.txt" {
		serveBytes(w, r, "text/plain; charset=utf-8", []byte{})
		return
	}
	packages, err := h.localPackages(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if path == "all_pods.txt" {
		names := make([]string, 0, len(packages))
		for name := range packages {
			names = append(names, name)
		}
		slices.Sort(names)
		serveBytes(w, r, "text/plain; charset=utf-8", lines(names))
		return
	}
	if strings.HasPrefix(path, "all_pods_versions_") && strings.HasSuffix(path, ".txt") {
		var entries []string
		for name, versions := range packages {
			if podfmt.ShardFile(name) != path {
				continue
			}
			v := make([]string, 0, len(versions))
			for number := range versions {
				v = append(v, number)
			}
			slices.Sort(v)
			entries = append(entries, name+"/"+strings.Join(v, "/"))
		}
		if len(entries) == 0 {
			h.k.Error(w, r, notFound())
			return
		}
		slices.Sort(entries)
		serveBytes(w, r, "text/plain; charset=utf-8", lines(entries))
		return
	}
	name, number, ok := parseSpecPath(path)
	if !ok {
		h.k.Error(w, r, notFound())
		return
	}
	v, ok := packages[name][number]
	if !ok {
		h.k.Error(w, r, notFound())
		return
	}
	data, err := specFor(baseurl.From(r.Context()), pkgroute.RequestFrom(r).Repo.Key, v.archive)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	serveBytes(w, r, "application/json", data)
}

func parseSpecPath(path string) (name, version string, ok bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 7 || parts[0] != "Specs" || !podfmt.ValidName(parts[4]) ||
		!podfmt.ValidVersion(parts[5]) || path != podfmt.SpecPath(parts[4], parts[5]) {
		return "", "", false
	}
	return parts[4], parts[5], true
}

func lines(values []string) []byte {
	if len(values) == 0 {
		return []byte{}
	}
	return []byte(strings.Join(values, "\n") + "\n")
}

func serveBytes(w http.ResponseWriter, r *http.Request, media string, data []byte) {
	sum := sha256.Sum256(data)
	w.Header().Set("Content-Type", media)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

func specFor(base, key string, a podfmt.Archive) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(a.Spec, &fields); err != nil {
		return nil, fmt.Errorf("cocoapods: indexed podspec: %w", err)
	}
	archiveURL := base + "/artifactory/api/pods/" + url.PathEscape(key) +
		"/packages/" + url.PathEscape(a.Name) + "/" + url.PathEscape(a.Version) + "/" +
		url.PathEscape(a.Name+"-"+a.Version+".tar.gz")
	source := map[string]string{"http": archiveURL}
	if a.SHA256 != "" {
		source["sha256"] = a.SHA256
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("cocoapods: source url: %w", err)
	}
	fields["source"] = encoded
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("cocoapods: podspec: %w", err)
	}
	return data, nil
}

// localPackages filters each version at its artifact path, including when a
// virtual member allows package-level listing but denies an individual file.
func (h handler) localPackages(r *http.Request) (map[string]map[string]version, error) {
	out := map[string]map[string]version{}
	key := pkgroute.RequestFrom(r).Repo.Key
	page := store.Page{Limit: store.MaxLimit}
	for {
		packages, next, err := h.k.DB().Packages().List(r.Context(), key, page)
		if err != nil {
			return nil, fmt.Errorf("cocoapods: list packages: %w", err)
		}
		for _, p := range packages {
			if !podfmt.ValidName(p.Name) {
				continue
			}
			versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
			if err != nil {
				return nil, fmt.Errorf("cocoapods: list versions: %w", err)
			}
			for _, v := range versions {
				if !h.k.Can(r, podfmt.Path(p.Name, v.Version), types.ActionRead) {
					continue
				}
				var a podfmt.Archive
				if json.Unmarshal(v.Metadata, &a) != nil || a.Name != p.Name ||
					a.Version != v.Version {
					return nil, fmt.Errorf(
						"%w: corrupt cocoapods index metadata for %s/%s", source.ErrInvalidResult,
						p.Name,
						v.Version,
					)
				}
				if out[p.Name] == nil {
					out[p.Name] = map[string]version{}
				}
				out[p.Name][v.Version] = version{archive: a, request: r}
			}
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

func (h handler) localArchive(w http.ResponseWriter, r *http.Request, path string) {
	name, number, ok := podfmt.ParsePath(path)
	if !ok {
		h.k.Error(w, r, notFound())
		return
	}
	packages, err := h.localPackages(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	v, ok := packages[name][number]
	if !ok {
		h.k.Error(w, r, notFound())
		return
	}
	h.k.ServeItem(w, v.request, path)
}
