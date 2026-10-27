// Package conan serves the Conan revision protocol at /artifactory/api/conan.
package conan

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	conanfmt "github.com/weaveplatform/artifactweave/internal/format/conan"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

type Options struct {
	RemoteSources *source.Service
	Tokens        *token.Service
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
		Name:         "conan",
		Types:        []repo.PackageType{repo.Conan},
		ErrorProfile: apierror.ProfileArtifactory,
		Reserved:     []string{"{repoKey}/reindex"},
		Sources:      sources,
		Challenge: func(c repo.Config) bool {
			return c.ForceConanAuthentication != nil && *c.ForceConanAuthentication
		},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, tokens: o.Tokens}
			r.HandleFunc("/", h.serve)
			r.HandleFunc("/*", h.serve)
		},
	}
}

type handler struct {
	k      *pkgroute.Kit
	tokens *token.Service
}

func (h handler) serve(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.error(w, r, err)
		return
	}
	if p.Folder || len(p.Props) != 0 {
		h.bad(w, r, "invalid Conan API path")
		return
	}
	s := p.Segments
	switch {
	case eq(s, "v1", "ping") || eq(s, "v2", "ping"):
		if r.Method != http.MethodGet {
			h.method(w, r)
			return
		}
		w.Header().Set("X-Conan-Server-Capabilities", "complex_search,revisions")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	case eq(s, "v2", "users", "authenticate") || eq(s, "v1", "users", "authenticate"):
		h.authenticate(w, r)
	case eq(s, "v2", "users", "check_credentials") || eq(s, "v1", "users", "check_credentials"):
		h.checkCredentials(w, r)
	case eq(s, "v2", "conans", "search"):
		h.search(w, r)
	case len(s) >= 6 && s[0] == "v2" && s[1] == "conans":
		h.conans(w, r, s)
	default:
		h.notFound(w, r)
	}
}

func (h handler) conans(w http.ResponseWriter, r *http.Request, s []string) {
	ref := conanfmt.Reference{Name: s[2], Version: s[3], User: s[4], Channel: s[5]}
	if !conanfmt.Component(ref.Name) || !conanfmt.Component(ref.Version) ||
		!conanfmt.Component(ref.User) || !conanfmt.Component(ref.Channel) {
		h.bad(w, r, "invalid Conan recipe reference")
		return
	}
	s = s[6:]
	switch {
	case eq(s, "latest"):
		h.recipeLatest(w, r, ref)
	case eq(s, "revisions"):
		h.recipeRevisions(w, r, ref)
	case eq(s, "search"):
		h.packageSearch(w, r, ref)
	case len(s) >= 2 && s[0] == "revisions":
		ref.Revision = s[1]
		if !conanfmt.Component(ref.Revision) {
			h.bad(w, r, "invalid Conan recipe revision")
			return
		}
		h.revision(w, r, ref, s[2:])
	default:
		h.notFound(w, r)
	}
}

func (h handler) revision(
	w http.ResponseWriter,
	r *http.Request,
	ref conanfmt.Reference,
	s []string,
) {
	switch {
	case len(s) == 0:
		h.deleteRecipe(w, r, ref)
	case eq(s, "files"):
		h.fileList(w, r, ref.RecipeRoot())
	case len(s) >= 2 && s[0] == "files":
		h.file(w, r, conanfmt.FilePath{Ref: ref, File: strings.Join(s[1:], "/")})
	case eq(s, "search"):
		h.packageSearch(w, r, ref)
	case eq(s, "packages"):
		h.deletePackages(w, r, ref)
	case len(s) >= 2 && s[0] == "packages":
		id := s[1]
		if !conanfmt.Component(id) {
			h.bad(w, r, "invalid Conan package ID")
			return
		}
		h.packageRoute(w, r, ref, id, s[2:])
	default:
		h.notFound(w, r)
	}
}

func (h handler) packageRoute(
	w http.ResponseWriter,
	r *http.Request,
	ref conanfmt.Reference,
	id string,
	s []string,
) {
	switch {
	case eq(s, "latest"):
		h.packageLatest(w, r, ref, id)
	case eq(s, "revisions"):
		h.packageRevisions(w, r, ref, id)
	case len(s) >= 2 && s[0] == "revisions":
		prev := s[1]
		if !conanfmt.Component(prev) {
			h.bad(w, r, "invalid Conan package revision")
			return
		}
		s = s[2:]
		f := conanfmt.FilePath{Ref: ref, Package: true, ID: id, PRev: prev}
		switch {
		case len(s) == 0:
			h.deletePackage(w, r, f)
		case eq(s, "files"):
			h.fileList(w, r, f.Root())
		case len(s) >= 2 && s[0] == "files":
			f.File = strings.Join(s[1:], "/")
			h.file(w, r, f)
		default:
			h.notFound(w, r)
		}
	default:
		h.notFound(w, r)
	}
}

func eq(got []string, want ...string) bool {
	return slices.Equal(got, want)
}

func (h handler) error(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, source.ErrInvalidResult) {
		err = apierror.New(http.StatusBadGateway, "Invalid Conan upstream response")
	}
	h.k.Error(w, r, err)
}

func (h handler) bad(w http.ResponseWriter, r *http.Request, message string) {
	h.error(w, r, apierror.New(http.StatusBadRequest, message))
}

func (h handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.error(w, r, apierror.New(http.StatusNotFound, "Conan resource not found"))
}

func (h handler) method(w http.ResponseWriter, r *http.Request) {
	h.error(w, r, apierror.New(http.StatusMethodNotAllowed, "Method not allowed"))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(v)
}
