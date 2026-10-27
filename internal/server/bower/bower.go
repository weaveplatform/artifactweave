// Package bower serves the Bower registry and bower-art-resolver 2.0 protocol.
package bower

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	bowerfmt "github.com/weaveplatform/artifactweave/internal/format/bower"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
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
		Name: "bower", Types: []repo.PackageType{repo.Bower},
		Surface: apierror.SurfaceArtifactory, Sources: sources,
		Reserved: []string{"{repoKey}/reindex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/packages", h.packages)
			r.Head("/packages", h.packages)
			r.Get("/packages/search/{name}", h.search)
			r.Head("/packages/search/{name}", h.search)
			r.Get("/packages/{name}", h.packageByName)
			r.Head("/packages/{name}", h.packageByName)
			r.Get("/refs/{owner}/{name}", h.refs)
			r.Head("/refs/{owner}/{name}", h.refs)
			r.Get("/binaries/{owner}/{git}/{tag}", h.binary)
			r.Head("/binaries/{owner}/{git}/{tag}", h.binary)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

type packageEntry struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func (h handler) entry(r *http.Request, name string) packageEntry {
	return packageEntry{Name: name, URL: "art://" + pkgroute.RequestFrom(r).Repo.Key + "/" + name}
}

func (h handler) packageByName(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !bowerfmt.ValidName(name) || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower package"))
		return
	}
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	versions, err := h.versions(r, name)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	if len(versions) == 0 {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Bower package not found"))
		return
	}
	jsonAnswer(w, r, h.entry(r, name))
}

func (h handler) search(w http.ResponseWriter, r *http.Request) {
	query := chi.URLParam(r, "name")
	if !bowerfmt.ValidName(query) || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower search"))
		return
	}
	h.list(w, r, query)
}

func (h handler) packages(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower package listing"))
		return
	}
	h.list(w, r, "")
}

func (h handler) list(w http.ResponseWriter, r *http.Request, query string) {
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	names, err := h.packageNames(r, query)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	answer := []packageEntry{}
	gitCatalog := pkgroute.RequestFrom(r).Repo.Class == repo.Remote && gitRemote(r)
	for _, name := range names {
		if query != "" && !strings.Contains(name, query) {
			continue
		}
		if gitCatalog {
			// Bower registry search lists registered names, including Git repos
			// without semver tags. Do not issue one smart-Git request per result.
			answer = append(answer, h.entry(r, name))
			continue
		}
		vs, err := h.versions(r, name)
		if err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
		if len(vs) > 0 {
			answer = append(answer, h.entry(r, name))
		}
	}
	jsonAnswer(w, r, answer)
}

func (h handler) refs(w http.ResponseWriter, r *http.Request) {
	name, _, ok := identity(r, false)
	if !ok || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower refs path"))
		return
	}
	if !h.k.RequireFolder(w, r, name, types.ActionRead) {
		return
	}
	versions, err := h.versions(r, name)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	if len(versions) == 0 {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Bower package not found"))
		return
	}
	var out strings.Builder
	for _, v := range versions {
		// bower-art-resolver 2.0.10 parses exactly 44 lowercase hex digits.
		// It uses the tag, not this opaque ref, for the binary URL.
		digest := sha256.Sum256([]byte(name + "\x00" + v))
		fmt.Fprintf(&out, "%x refs/tags/v%s\n", digest[:22], v)
	}
	textAnswer(w, r, out.String())
}

func (h handler) binary(w http.ResponseWriter, r *http.Request) {
	name, tag, ok := identity(r, true)
	if !ok || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower binary path"))
		return
	}
	version := strings.TrimPrefix(tag, "v")
	if !bowerfmt.ValidVersion(version) {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower version"))
		return
	}
	if !h.k.Require(w, r, bowerfmt.ManifestPath(name, version), types.ActionRead) {
		return
	}
	versions, err := h.versions(r, name)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	if !slices.Contains(versions, version) {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Bower version not found"))
		return
	}
	h.serveBinary(w, r, name, version, tag)
}

func identity(r *http.Request, binary bool) (name, tag string, ok bool) {
	owner := chi.URLParam(r, "owner")
	name = chi.URLParam(r, "name")
	if binary {
		name = strings.TrimSuffix(chi.URLParam(r, "git"), ".git")
	}
	if !bowerfmt.ValidName(owner) || owner != pkgroute.RequestFrom(r).Repo.Key ||
		!bowerfmt.ValidName(name) {
		return "", "", false
	}
	if binary {
		git := chi.URLParam(r, "git")
		tag = chi.URLParam(r, "tag")
		if git != name+".git" || tag == "" || strings.ContainsAny(tag, `/\`) {
			return "", "", false
		}
	}
	return name, tag, true
}

func (h handler) packageNames(r *http.Request, query string) ([]string, error) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		return h.virtualPackageNames(r, query)
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		return h.remotePackageNames(r, query)
	}
	var out []string
	for page := (store.Page{Limit: store.MaxLimit}); ; {
		ps, next, err := h.k.DB().
			Packages().
			List(r.Context(), pkgroute.RequestFrom(r).Repo.Key, page)
		if err != nil {
			return nil, fmt.Errorf("bower list packages: %w", err)
		}
		for _, p := range ps {
			out = append(out, p.Name)
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

func (h handler) versions(r *http.Request, name string) ([]string, error) {
	if !h.k.CanFolder(r, name, types.ActionRead) {
		return nil, nil
	}
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Virtual:
		return h.virtualVersions(r, name)
	case repo.Remote:
		return h.remoteVersions(r, name)
	default:
		p, err := h.k.DB().Packages().Find(r.Context(), pkgroute.RequestFrom(r).Repo.Key, name)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("bower find package: %w", err)
		}
		vs, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
		if err != nil {
			return nil, fmt.Errorf("bower list versions: %w", err)
		}
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			if h.k.Can(r, bowerfmt.ManifestPath(name, v.Version), types.ActionRead) {
				out = append(out, v.Version)
			}
		}
		return out, nil
	}
}

func jsonAnswer(w http.ResponseWriter, r *http.Request, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func textAnswer(w http.ResponseWriter, r *http.Request, value string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(value))
	}
}

// Virtual is resolved by this package because the package index and binary
// representation are computed from ordered members, not one stored item.
type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	store.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}
