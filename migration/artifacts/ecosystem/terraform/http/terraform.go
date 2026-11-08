package terraformhttp

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
)

const prefix = "/artifactory/api/terraform/"

// Options provides the shared master key for short-lived download URLs and
// the access contracts that re-evaluate a URL's subject each time it is used
// (decision 0040). The contracts read through the request's context, so a
// server binds them over the store the router kit serves. Now is a test
// clock; nil uses the wall clock.
type Options struct {
	// Authenticator rechecks the credential that a remote module or
	// provider download URL names. Nil, as on a server without
	// authentication, refuses those URLs 403.
	Authenticator accessapi.Authenticator
	// Authorizer evaluates the permissions of the principal a download URL
	// names. Serving any signed download requires it.
	Authorizer accessapi.Authorizer
	// KeyPairs reads the GPG key pairs a provider repository names as its
	// signing keys, for a provider's download metadata. A repository that
	// names a key pair requires it.
	KeyPairs accessapi.Keys
	// Restrictions, Identity and AnonymousAccess resolve the user a local
	// download URL names: its API restriction, whether its account still
	// exists and is enabled, and, for the anonymous user, whether anonymous
	// access is on. Serving a local signed download requires all three.
	Restrictions    accessapi.Restrictions
	Identity        accessapi.Identity
	AnonymousAccess accessapi.AnonymousAccess
	RemoteSources   *source.Service
	Keys            *crypt.Keyring
	Now             func() time.Time
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
