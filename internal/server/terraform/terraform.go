// Package terraform serves Terraform's discovery and module registry
// through the package router kit (decision 0040).
package terraform

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const prefix = "/artifactory/api/terraform/"

// Options provides the shared master key for short-lived download URLs.
// Now is a test clock; nil uses the wall clock.
type Options struct {
	Auth          *auth.Authenticator
	RemoteSources *source.Service
	Keys          *crypt.Keyring
	Now           func() time.Time
}

// Protocol mounts the registry and anonymous host-root discovery document.
func Protocol(o Options) pkgroute.Protocol {
	if o.Now == nil {
		o.Now = time.Now
	}
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Sources:      sources,
		Name:         "terraform",
		Types:        []repo.PackageType{repo.Terraform},
		ErrorProfile: apierror.ProfileTerraformRegistry,
		Reserved:     []string{"{repoKey}/reindex"},
		Resolve:      resolve,
		Signed:       func(r *http.Request) bool { return r.URL.Query().Has("sig") },
		Host: []pkgroute.HostRoute{
			{
				Path:            "/.well-known/terraform.json",
				Methods:         []string{"GET", "HEAD"},
				Unauthenticated: true,
				Serve: func(w http.ResponseWriter, r *http.Request, _ *pkgroute.Kit) {
					base := baseurl.From(r.Context()) + prefix + "v1/"
					writeJSON(
						w,
						r,
						map[string]string{
							"modules.v1":   base + "modules/",
							"providers.v1": base + "providers/",
							"state.v2":     baseurl.From(r.Context()) + prefix + "remote/v2",
							"tfe.v2":       baseurl.From(r.Context()) + prefix + "remote/v2",
							"tfe.v2.1":     baseurl.From(r.Context()) + prefix + "remote/v2",
							"tfe.v2.2":     baseurl.From(r.Context()) + prefix + "remote/v2",
						},
					)
				},
			},
		},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, o: o}
			r.HandleFunc("/*", h.serve)
		},
	}
}

func resolve(rel string) []pkgroute.Key {
	for _, kind := range []struct{ path, sep string }{{"v1/modules/", "__"}, {"v1/providers/", "-rt-ns-"}} {
		if rest, ok := strings.CutPrefix(rel, kind.path); ok {
			ns, tail, _ := strings.Cut(rest, "/")
			keys := pkgroute.SplitKey(ns, kind.sep)
			for i := range keys {
				keys[i].Rest = kind.path + keys[i].Rest + "/" + tail
			}
			return keys
		}
	}
	return pkgroute.FirstSegment(rel)
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func missing() error {
	return apierror.New(http.StatusNotFound, "Terraform module or operation not found")
}
