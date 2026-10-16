package docker

import (
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Request is a registry request with its operation parsed and, for an
// operation on an image, its repository resolved and its name validated.
type Request struct {
	Kind Kind
	// RepoKey is the repository the request addressed: the subdomain, or
	// the key in /artifactory/api/docker/<key>/v2. It is empty for /v2/,
	// /v2/_catalog and /v2/token on the repository path, which address no
	// repository, and may be "null", which the token realm uses for that
	// (research 17). Only an operation on an image resolves it into Repo.
	RepoKey string
	// Repo is the resolved repository, set when Kind is an image operation.
	Repo repo.Config
	// Name is the image name inside the repository.
	Name string
	// Reference is the digest, tag or upload session the path ends in.
	Reference string
	// base is the path up to the image name in the form the request used,
	// so that Path answers in the same form.
	base string
	// resolved is set once Repo holds the repository RepoKey names.
	resolved bool
	// scope is the image's name as the client wrote it, when base does not
	// imply it: a mount source under the Artifactory prefix named in the
	// repository form, <repo>/<image>.
	scope string
}

// Path returns the absolute path of an operation on the request's image in
// the addressing form the client used, for Location headers: the client
// may only know the registry by that form.
// Path("blobs", "uploads", id) is .../<name>/blobs/uploads/<id>.
func (q Request) Path(segments ...string) string {
	return q.base + "/" + q.Name + "/" + strings.Join(segments, "/")
}

// Require answers nil when the caller holds every action in want on path,
// a path inside the repository. Otherwise it answers what the registry
// tells a client: UNAUTHORIZED to an anonymous caller, so that it sends
// credentials, and DENIED to anyone else. A registry token must also
// grant the actions for the image as this request names it; one that does
// not gets UNAUTHORIZED, whose challenge (Challenge) names the scope the
// client then fetches a token for, as Docker's registry answers an
// insufficient scope. A request without a principal reached the handler
// only on a server built without authentication, which only tests build,
// and is allowed.
func (q Request) Require(r *http.Request, path string, want types.Actions) error {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Access() == nil {
		return nil
	}
	if p.Registry != nil && !q.granted(p.Registry, r.Method, want) {
		return Errorf(http.StatusUnauthorized, CodeUnauthorized,
			"the token does not grant this access to %s", q.scopeName())
	}
	if p.Access().Allows(types.ResourceArtifact, authz.Repo{
		Key: q.Repo.Key, Class: string(q.Repo.Class),
	}, path, want) {
		return nil
	}
	if p.Anonymous {
		return Errorf(http.StatusUnauthorized, CodeUnauthorized, "authentication required")
	}
	return Errorf(http.StatusForbidden, CodeDenied,
		"requested access to %s in %s is denied", q.Name, q.Repo.Key)
}

// scopeName is the image's name as the client wrote it, the name of its
// token scope: on the repository path it begins with the repository key.
func (q Request) scopeName() string {
	if q.scope != "" {
		return q.scope
	}
	if strings.HasPrefix(q.base, "/v2/") {
		return q.RepoKey + "/" + q.Name
	}
	return q.Name
}

// granted reports whether a registry token grants every action want needs
// on the request's image. A grant binds the name as the client wrote it
// and the realm it was issued under: null names repository-path names,
// any other realm names that repository's images. Read needs pull,
// Deploy and Annotate push, and Delete delete on a DELETE but push
// otherwise: a scope names what the client does, and replacing a tag is
// part of a push, which Docker asks pull,push for (the permissions still
// require Delete, DV-035). Manage is never granted.
func (q Request) granted(a *types.RegistryAccess, method string, want types.Actions) bool {
	pathMethod := strings.HasPrefix(q.base, "/v2/")
	if a.Realm == "" && !pathMethod || a.Realm != "" && !strings.EqualFold(a.Realm, q.RepoKey) {
		return false
	}
	need := map[types.Actions]string{
		types.ActionRead: actionPull, types.ActionWrite: actionPush,
		types.ActionAnnotate: actionPush, types.ActionDelete: actionPush,
	}
	if method == http.MethodDelete {
		need[types.ActionDelete] = actionDelete
	}
	name := q.scopeName()
	for bit := types.Actions(1); bit != 0 && bit <= want; bit <<= 1 {
		if want&bit == 0 {
			continue
		}
		action, known := need[bit]
		if !known || !slices.ContainsFunc(a.Grants, func(g types.RegistryGrant) bool {
			return g.Type == scopeRepository && strings.EqualFold(g.Name, name) &&
				(slices.Contains(g.Actions, action) || slices.Contains(g.Actions, actionAll))
		}) {
			return false
		}
	}
	return true
}

// GrantsCatalog reports whether the request's credential may list the
// catalog: any but a registry token without registry:catalog:*.
func GrantsCatalog(r *http.Request) bool {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Registry == nil {
		return true
	}
	return slices.ContainsFunc(p.Registry.Grants, func(g types.RegistryGrant) bool {
		return g.Type == scopeRegistry && g.Name == "catalog" && len(g.Actions) > 0
	})
}

// readable reports whether the caller may read path in c, for listings
// that leave out what the caller cannot read rather than refuse; a request
// without a principal, as Require allows it, reads everything.
func readable(r *http.Request, c repo.Config, path string) bool {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Access() == nil {
		return true
	}
	return p.Access().Allows(types.ResourceArtifact, authz.Repo{
		Key: c.Key, Class: string(c.Class),
	}, path, types.ActionRead)
}
