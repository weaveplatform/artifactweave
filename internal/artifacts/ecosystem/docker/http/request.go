package dockerhttp

import (
	"net/http"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
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
	// isResolved is set once Repo holds the repository RepoKey names.
	isResolved bool
	// scope is the image's name as the client wrote it, when base does not
	// imply it: a mount source under the Artifactory prefix named in the
	// repository form, <repo>/<image>.
	scope string
	// authenticated is the registry's Options.Authenticated, which decides
	// how Require answers a request without a decider.
	authenticated bool
}

// Path returns the absolute path of an operation on the request's image in
// the addressing form the client used, for Location headers: the client
// may only know the registry by that form.
// Path("blobs", "uploads", id) is .../<name>/blobs/uploads/<id>.
func (q Request) Path(segments ...string) string {
	return q.base + "/" + q.Name + "/" + strings.Join(segments, "/")
}

// Require answers nil when the caller holds every action in want on path,
// a path inside the repository, and a registry token it presents grants
// them for the image as this request names it. A caller the permissions
// refuse gets 7.x's refusal (refuse). The permissions are asked before the
// token's grant: the token endpoint leaves out what the caller may not do,
// so the other way round a docker pull by a user without Read would answer
// the token's 401 instead of the 403 every 7.x capture of a refused pull
// shows, and containerd would fetch a token and ask again up to five
// times. A token without an action the caller does hold gets UNAUTHORIZED
// with 401, whose challenge (Challenge) names the scope the client then
// fetches a token for, as Docker's registry answers an insufficient scope.
// A request without a permission decider reaches the handler on a server
// built without authentication, which only tests build, and is allowed. On
// a server that authenticates every request (Options.Authenticated) a
// principal stored without its decider is refused as one without the
// right, so that it is never allowed everything, and a request with no
// principal at all as one without credentials.
func (q Request) Require(r *http.Request, path string, want accessapi.Actions) error {
	p, ok := kernel.FromContext(r.Context())
	d, _ := accessapi.DeciderFrom(r.Context())
	if d == nil {
		switch {
		case !q.authenticated:
			return nil
		case !ok:
			return Errorf(http.StatusUnauthorized, CodeUnauthorized, "authentication required")
		}
		return q.refuse(r.Method, p)
	}
	if !d.Allows(accessapi.ResourceArtifact, accessapi.Repo{
		Key: q.Repo.Key, Class: string(q.Repo.Class),
	}, path, want) {
		return q.refuse(r.Method, p)
	}
	if p.Registry != nil && !q.granted(p.Registry, r.Method, want) {
		return Errorf(http.StatusUnauthorized, CodeUnauthorized,
			"the token does not grant this access to %s", q.scopeName())
	}
	return nil
}

// refuse answers a caller whose permissions refuse the request as 7.x
// does: 403 with the code UNAUTHORIZED, not the reference registry's
// DENIED, and 7.x's message. The Docker daemon prints UNAUTHORIZED as
// "unauthorized: <message>"; DENIED it would print as "pull access denied
// ... may require 'docker login'", which 7.x keeps for an Xray block
// (RTFACT-26140, RTFACT-30133).
//
// A pull names what it reads, as docker printed 7.x's 403 on 7.38.8 and
// later (RTFACT-27228, RTFACT-27049, RTFACT-30133, RTFACT-30774): "The
// client does not have permission for manifest: Download request for
// repo:path '<repo>:<path>' is forbidden for user: '<user>'." A push gets
// "The client does not have permission to push to the repository.", the
// distribution specification's text for a manifest PUT, which docker
// printed for a manifest and a layer on 5.5.1 to 6.5.2 (RTFACT-15115,
// RTFACT-17727, RTFACT-17858, RTFACT-21122) [I]. An anonymous caller is
// refused the same way, with 403, not 401: anonymous access is on for it
// to have got this far, and 6.19 and 6.20 answered an anonymous pull 403
// with this message and the user 'anonymous' (RTFACT-21825, RTFACT-22867)
// [I]. A 401 would only make containerd fetch another anonymous token and
// ask again.
func (q Request) refuse(method string, p kernel.Principal) error {
	switch scopeActions(q.Kind, method) {
	case actionPull:
		return Errorf(http.StatusForbidden, CodeUnauthorized,
			"The client does not have permission for %s: Download request for repo:path "+
				"'%s:%s' is forbidden for user: '%s'.",
			q.Kind.noun(), q.storageKey(), q.storagePath(), refusedUser(p))
	case actionDelete:
		// No capture shows 7.x refusing a delete; the sentence follows the
		// push's [I].
		return Errorf(http.StatusForbidden, CodeUnauthorized,
			"The client does not have permission to delete from the repository.")
	default:
		return Errorf(http.StatusForbidden, CodeUnauthorized,
			"The client does not have permission to push to the repository.")
	}
}

// storageKey is the repository 7.x names in a refused pull: a remote
// repository's cache, <key>-cache, which every capture of a remote names
// (RTFACT-27228, RTFACT-30774), and any other repository's own key.
func (q Request) storageKey() string {
	if q.Repo.Class == repo.Remote {
		return q.Repo.Key + "-cache"
	}
	return q.Repo.Key
}

// storagePath is the path 7.x names in a refused pull: the file the pull
// reads, as the layout stores it. A manifest by tag is
// <image>/<tag>/manifest.json, as 7.x named a remote's (RTFACT-27228,
// RTFACT-30774). By digest it is in the digest folder: a remote's cache
// names it sha256__<hex>, as 7.x did (RTFACT-30133), and a local
// repository sha256:<hex>, the folder its layout keeps a manifest pushed by
// digest in [I]. 7.x names list.manifest.json for an index, which only a
// lookup tells; the caller may not read the image, so none is made [I]. A
// blob is <image>/sha256__<hex>, its folder unknown before a lookup too
// [I]; a tags list and referrers name the image [I].
func (q Request) storagePath() string {
	hex, digest := layout.SHA256Hex(q.Reference)
	switch {
	case q.Kind == KindManifest && digest && q.Repo.Class == repo.Remote:
		return q.Name + "/" + layout.BlobFile(hex) + "/" + layout.ManifestFile
	case q.Kind == KindManifest && digest:
		return q.Name + "/" + layout.DigestFolder(hex) + "/" + layout.ManifestFile
	case q.Kind == KindManifest:
		return q.Name + "/" + q.Reference + "/" + layout.ManifestFile
	case q.Kind == KindBlob && digest:
		return q.Name + "/" + layout.BlobFile(hex)
	}
	return q.Name
}

// noun is what a refused pull of the kind says it lacks permission for:
// manifest, as 7.x says (RTFACT-27228), and by the same rule blob, tags
// and referrers, which no capture shows [I].
func (k Kind) noun() string {
	switch k {
	case KindManifest:
		return "manifest"
	case KindBlob:
		return "blob"
	case KindTags:
		return "tags"
	default:
		return "referrers"
	}
}

// refusedUser is the user 7.x names in a refused pull: the user's name, or
// token:<name> for a token whose user need not be stored, a transient or
// synthetic identity, as 7.x named a transient user's token on 7.38.8
// (RTFACT-27228) and an OIDC-mapped one (RTFACT-30774). An anonymous
// caller is 'anonymous' (RTFACT-21825).
func refusedUser(p kernel.Principal) string {
	switch {
	case p.Anonymous || p.Name == "":
		return kernel.AnonymousUser
	case p.Identity == kernel.IdentityTransient || p.Identity == kernel.IdentitySynthetic:
		return "token:" + p.Name
	}
	return p.Name
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
func (q Request) granted(a *kernel.RegistryAccess, method string, want accessapi.Actions) bool {
	pathMethod := strings.HasPrefix(q.base, "/v2/")
	if a.Realm == "" && !pathMethod || a.Realm != "" && !strings.EqualFold(a.Realm, q.RepoKey) {
		return false
	}
	need := map[accessapi.Actions]string{
		accessapi.ActionRead: actionPull, accessapi.ActionWrite: actionPush,
		accessapi.ActionAnnotate: actionPush, accessapi.ActionDelete: actionPush,
	}
	if method == http.MethodDelete {
		need[accessapi.ActionDelete] = actionDelete
	}
	name := q.scopeName()
	for bit := accessapi.Actions(1); bit != 0 && bit <= want; bit <<= 1 {
		if want&bit == 0 {
			continue
		}
		action, known := need[bit]
		if !known || !slices.ContainsFunc(a.Grants, func(g kernel.RegistryGrant) bool {
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
	p, ok := kernel.FromContext(r.Context())
	if !ok || p.Registry == nil {
		return true
	}
	return slices.ContainsFunc(p.Registry.Grants, func(g kernel.RegistryGrant) bool {
		return g.Type == scopeRegistry && g.Name == "catalog" && len(g.Actions) > 0
	})
}

// readsAnything reports whether the caller may read some path in c, which
// lets it list c's images in a catalog; a request without a decider reads
// everything exactly where Require allows it, on a server built without
// authentication. The repository's root is asked as a folder, which a grant
// on any path below it makes readable.
func (g *Registry) readsAnything(r *http.Request, c repo.Config) bool {
	d, _ := accessapi.DeciderFrom(r.Context())
	if d == nil {
		return !g.authenticated
	}
	return d.AllowsFolder(accessapi.ResourceArtifact, accessapi.Repo{
		Key: c.Key, Class: string(c.Class),
	}, "", accessapi.ActionRead)
}
