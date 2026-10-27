// Package docker serves the registry protocol, the OCI Distribution
// Specification v1.1 with Docker's extensions, for docker, oci and helmoci
// repositories (decisions 0003 and 0031). It answers three addressing forms:
//
//   - the repository path, /v2/<repo>/<image>/..., the native default, which
//     needs no reverse proxy;
//   - Artifactory's own prefix, /artifactory/api/docker/<repo>/v2/<image>/...;
//   - the subdomain, <repo>.<base_url host>/v2/<image>/..., when
//     docker.subdomain_match is on.
//
// The router is hand-written and mounted before the generated servers
// (decision 0002). It parses each path into a Request and hands it to the
// endpoint a feature registered for its kind and method; an operation the
// specification defines that no feature has registered yet answers 405
// UNSUPPORTED. This package never imports internal/server.
package docker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/format"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// APIVersionHeader is the header every registry response carries. Docker
// clients read it from the /v2/ probe, the 401 included, to know that they
// reached a v2 registry.
const (
	APIVersionHeader = "Docker-Distribution-API-Version"
	apiVersion       = "registry/2.0"
)

// RegistryHeader names, on a manifest read, the repository that holds the
// manifest, by its stored key. jf docker push and pull HEAD the manifest
// through the registry after the docker client finishes and read the key
// from here; without it they fail before collecting build-info
// (jfrog-cli-artifactory ocicontainer/image.go, GetRemoteRepo).
const RegistryHeader = "X-Artifactory-Docker-Registry"

// prefix is Artifactory's own path to a repository's registry.
const prefix = "/artifactory/api/docker/"

// Options configures a Registry.
type Options struct {
	Sources *source.Service
	// Store resolves repositories. Nil serves no repository, for tests of
	// the router alone.
	Store store.Store
	// SubdomainMatch turns on the subdomain method. BaseURL must then be
	// set: its host is the parent the repository subdomains sit under.
	SubdomainMatch bool
	BaseURL        string
	// Fault records an unexpected error for the access log, as the server's
	// handlers do; nil drops it.
	Fault func(*http.Request, error)
	// Blobs is the blob store behind Store. Nil leaves blobs and uploads
	// unimplemented, for tests of the router alone.
	Blobs blob.Store
	// ServeBlob answers a GET of a blob's item in a repository with the
	// server's download, which applies the download redirect policy
	// (decision 0029) and counts the download. Nil streams it from Blobs.
	ServeBlob func(w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item)
	// RedirectManifest answers a GET of a manifest's item, whose media type
	// is mediaType, with the server's download redirect when the policy says
	// so (decision 0029), and reports whether it wrote a response. Nil
	// streams every manifest.
	RedirectManifest func(
		w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item, mediaType string,
	) bool
	// Now supplies the time to upload sessions; nil means time.Now.
	Now func() time.Time
	// Tokens signs registry tokens; nil leaves the token endpoint
	// unimplemented.
	Tokens TokenIssuer
}

// Endpoint serves one registry operation for a parsed request.
type Endpoint func(w http.ResponseWriter, r *http.Request, q Request)

// Registry routes registry requests to the endpoints features register.
type Registry struct {
	sources *source.Service
	store   store.Store
	// parent is the lower-cased host repository subdomains sit under; empty
	// when subdomain matching is off.
	parent    string
	fault     func(*http.Request, error)
	tokens    TokenIssuer
	endpoints map[endpointKey]Endpoint
	// blobs, uploads and serveBlob serve blobs; uploads is nil without a
	// blob store.
	blobs            blob.Store
	uploads          *registry.Service
	serveBlob        func(http.ResponseWriter, *http.Request, repo.Config, types.Item)
	redirectManifest func(http.ResponseWriter, *http.Request, repo.Config, types.Item, string) bool
}

type endpointKey struct {
	kind   Kind
	method string
}

// New returns the registry with every implemented endpoint registered.
func New(o Options) *Registry {
	g := &Registry{
		sources: o.Sources,
		store:   o.Store, fault: o.Fault, tokens: o.Tokens, endpoints: map[endpointKey]Endpoint{},
	}
	if o.SubdomainMatch {
		g.parent = baseHost(o.BaseURL)
	}
	if o.Store != nil && o.Blobs != nil {
		g.blobs = o.Blobs
		g.uploads = &registry.Service{Store: o.Store, Blobs: o.Blobs, Now: o.Now}
		g.serveBlob = o.ServeBlob
		if g.serveBlob == nil {
			g.serveBlob = g.streamBlob
		}
		g.redirectManifest = o.RedirectManifest
	}
	// Each feature registers its endpoints with one call here.
	g.registerProbe()
	g.registerBlobs()
	g.registerManifests()
	g.registerToken()
	g.registerAuth()
	g.registerReferrers()
	g.registerListings()
	return g
}

// handle registers ep for a kind and method. Features call it from their
// register function.
func (g *Registry) handle(k Kind, method string, ep Endpoint) {
	g.endpoints[endpointKey{k, method}] = ep
}

// Register mounts the registry on r for every method, so that the
// registry answers 405 itself, in its own envelope. Under the Artifactory
// prefix chi prefers the generated JFrog operations' literal segments
// (POST parentManifests, POST promote, DELETE delete) to these catch-alls,
// so those keep their generated handlers (decision 0003). The JFrog
// catalog and tags list are the registry's own operations, marked
// x-handwritten in the specification; they are registered under the
// patterns the specification gives them as well, so that the server's
// stubs for unimplemented hand-written operations never claim them.
func (g *Registry) Register(r chi.Router) {
	r.HandleFunc("/v2", g.serveRoot)
	r.HandleFunc("/v2/*", g.serveRoot)
	r.HandleFunc(prefix+"{repoKey}/v2", g.servePrefix)
	r.HandleFunc(prefix+"{repoKey}/v2/*", g.servePrefix)
	r.Get(prefix+"{repoKey}/v1/auth", g.serveV1Auth)
	r.HandleFunc(prefix+"{repo-key}/v2/_catalog", g.servePrefix)
	r.HandleFunc(prefix+"{repo-key}/v2/{imageName}/tags/list", g.servePrefix)
}

// serveRoot serves /v2/..., by subdomain or by repository path.
func (g *Registry) serveRoot(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v2"), "/")
	t, ok := parseTarget(rest)
	if !ok {
		notFound(w, r)
		return
	}
	q := Request{Kind: t.kind, Reference: t.ref}
	c, sub, err := g.subdomain(r)
	switch {
	case err != nil:
		g.writeErr(w, r, err)
		return
	case sub:
		q.RepoKey, q.Repo, q.isResolved, q.Name, q.base = c.Key, c, true, t.image, "/v2"
		if t.kind == kindRepoCatalog {
			// <x>/_catalog names a repository; a subdomain already has one.
			notFound(w, r)
			return
		}
	case t.kind.image():
		// The repository is the first segment and the image the rest.
		q.RepoKey, q.Name, _ = strings.Cut(t.image, "/")
		q.base = "/v2/" + q.RepoKey
	case t.kind == kindRepoCatalog:
		q.RepoKey = t.image
	}
	if q.Kind == kindRepoCatalog {
		q.Kind = KindCatalog
	}
	g.dispatch(w, r, q)
}

// servePrefix serves /artifactory/api/docker/<repo>/v2/....
func (g *Registry) servePrefix(w http.ResponseWriter, r *http.Request) {
	key, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, prefix), "/")
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, "v2"), "/")
	if method, ok := jfrogOperations[rest]; ok {
		// chi routes a JFrog operation to the generated server ahead of
		// this catch-all; reaching it means a method no route serves, or an
		// operation not specified yet. Answer as the server would.
		e := apierror.New(
			http.StatusMethodNotAllowed,
			"method not allowed: "+r.Method+" "+r.URL.Path,
		)
		if r.Method == method {
			e = apierror.New(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path)
		}
		apierror.Write(w, apierror.ProfileArtifactory, e)
		return
	}
	t, ok := parseTarget(rest)
	if rest == "auth" {
		t, ok = target{kind: KindAuth}, true
	}
	if !ok || t.kind == kindRepoCatalog {
		notFound(w, r)
		return
	}
	g.dispatch(w, r, Request{
		Kind: t.kind, RepoKey: key, Name: t.image, Reference: t.ref, base: prefix + key + "/v2",
	})
}

// dispatch checks the method, resolves the repository of a catalog or an
// image operation and validates the image name, then calls the endpoint
// registered for the kind and method.
func (g *Registry) dispatch(w http.ResponseWriter, r *http.Request, q Request) {
	allowed := methods[q.Kind]
	if !slices.Contains(allowed, r.Method) {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		Write(w, Errorf(http.StatusMethodNotAllowed, CodeUnsupported,
			"%s does not answer %s", q.Kind, r.Method))
		return
	}
	if q.Kind.image() || (q.Kind == KindCatalog && q.RepoKey != "") {
		if err := g.resolveInto(r.Context(), &q); err != nil {
			g.writeErr(w, r, err)
			return
		}
	}
	if q.Kind.image() && !ValidName(q.Name) {
		Write(w, Errorf(http.StatusBadRequest, CodeNameInvalid,
			"invalid image name %q: reference images as <host>/<repository>/<image>", q.Name))
		return
	}
	if q.Repo.Class == repo.Remote && (q.Kind.image() || q.Kind == KindCatalog) {
		g.serveRemote(w, r, q)
		return
	}
	ep, ok := g.endpoints[endpointKey{q.Kind, r.Method}]
	if !ok {
		Write(w, Errorf(http.StatusMethodNotAllowed, CodeUnsupported,
			"%s %s is not implemented by artifactweave yet", r.Method, q.Kind))
		return
	}
	ep(w, r, q)
}

// methods are the methods the specification defines for each kind. Another
// method answers 405 with Allow; a defined one no feature has registered
// answers 405 too, with a message saying so. The token endpoint answers
// GET alone (research 17).
var methods = map[Kind][]string{
	KindBase:      {http.MethodGet, http.MethodHead},
	KindCatalog:   {http.MethodGet},
	KindToken:     {http.MethodGet},
	KindAuth:      {http.MethodGet},
	KindBlob:      {http.MethodGet, http.MethodHead, http.MethodDelete},
	KindUploads:   {http.MethodPost},
	KindUpload:    {http.MethodGet, http.MethodPatch, http.MethodPut, http.MethodDelete},
	KindManifest:  {http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete},
	KindTags:      {http.MethodGet},
	KindReferrers: {http.MethodGet},
}

// resolveInto sets q.Repo to the repository q.RepoKey names, unless the
// subdomain already did. Remote content requires its adapter; virtual member
// resolution remains a separate P10 capability.
func (g *Registry) resolveInto(ctx context.Context, q *Request) error {
	if !q.isResolved && g.store == nil {
		// A router built without a store, as the server's routing tests
		// build it, answers like an unimplemented operation, as the
		// server's own handlers do without one.
		return Errorf(http.StatusNotImplemented, CodeUnsupported,
			"%s is not available without a store", q.Kind)
	}
	if !q.isResolved {
		c, found, err := g.lookup(ctx, q.RepoKey)
		if err != nil {
			return err
		}
		if !found {
			return nameUnknown(*q)
		}
		q.Repo, q.isResolved = c, true
	}
	switch q.Repo.Class {
	case repo.Remote:
		if g.sources != nil {
			return nil
		}
		fallthrough
	case repo.Virtual:
		return Errorf(http.StatusMethodNotAllowed, CodeUnsupported,
			"%s is a %s repository, which the registry does not serve yet",
			q.Repo.Key, q.Repo.Class)
	default:
		return nil
	}
}

// lookup is Lookup in the registry's store.
func (g *Registry) lookup(ctx context.Context, key string) (repo.Config, bool, error) {
	if g.store == nil || key == "" {
		return repo.Config{}, false, nil
	}
	return Lookup(ctx, g.store, key)
}

// Lookup finds the Docker, OCI or Helm OCI repository key names in st,
// compared without regard to case: keys are unique that way, and Docker
// clients send references in lower case only (decision 0003). found is
// false for a missing repository and for one of another package type.
func Lookup(ctx context.Context, st store.Store, key string) (repo.Config, bool, error) {
	c, err := st.Repos().Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		// The exact key failed; a case-insensitive match needs the list.
		var all []repo.Config
		if all, err = st.Repos().List(ctx, store.RepoFilter{}); err == nil {
			i := slices.IndexFunc(
				all,
				func(c repo.Config) bool { return strings.EqualFold(c.Key, key) },
			)
			if i < 0 {
				return repo.Config{}, false, nil
			}
			c = all[i]
		}
	}
	if err != nil {
		return repo.Config{}, false, fmt.Errorf("get repository %s: %w", key, err)
	}
	return c, Serves(c.PackageType), nil
}

// Serves reports whether the registry serves repositories of package type
// t: docker, and the oci and helmoci types that share its protocol
// (research 04).
func Serves(t repo.PackageType) bool { return layout.Serves(t) }

// notFound answers a path that is no registry operation: 404 UNSUPPORTED
// (decision 0031).
func notFound(w http.ResponseWriter, r *http.Request) {
	Write(w, Errorf(http.StatusNotFound, CodeUnsupported,
		"not a registry operation: %s %s", r.Method, r.URL.Path))
}

// blackedOut answers a pull from a blacked-out repository 404 NAME_UNKNOWN
// with Artifactory's message, as a push to it is answered, before the item
// is looked for: every Artifactory download is refused that way
// (generic.CheckDownload).
func (g *Registry) blackedOut(w http.ResponseWriter, r *http.Request, q Request) bool {
	if err := generic.CheckBlackedOut(q.Repo, q.Name); err != nil {
		g.writeErr(w, r, err)
		return true
	}
	return false
}

// excluded answers a pulled item the repository's include and exclude
// patterns reject as a missing one, 404 under code, with Artifactory's
// message.
func excluded(w http.ResponseWriter, q Request, it types.Item, code string) bool {
	var refused *format.RejectionError
	if errors.As(generic.CheckDownload(q.Repo, it.FullPath(), false), &refused) {
		Write(w, Errorf(http.StatusNotFound, code, "%s", refused.Message))
		return true
	}
	return false
}

// writeErr writes err, noting an unexpected one for the access log. A
// format's RejectionError, such as a push to a blacked-out repository, keeps its
// status and message under the registry code a client reads for it.
func (g *Registry) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var refused *format.RejectionError
	if errors.As(err, &refused) {
		code := CodeDenied
		if refused.Status == http.StatusNotFound {
			code = CodeNameUnknown
		}
		Write(w, Errorf(refused.Status, code, "%s", refused.Message))
		return
	}
	var typed *apierror.Error
	if !errors.As(err, &typed) && g.fault != nil {
		g.fault(r, err)
	}
	Write(w, err)
}

// APIVersion sets the version header on every registry response, before
// authentication can answer one.
func APIVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsRegistryPath(r.URL.Path) {
			w.Header().Set(APIVersionHeader, apiVersion)
		}
		next.ServeHTTP(w, r)
	})
}

// IsRegistryPath reports whether path is under /v2 or a repository's
// /artifactory/api/docker/<repo>/v2.
func IsRegistryPath(path string) bool {
	_, ok := registryRest(path)
	return ok
}

// ChallengesAnonymous reports whether a request to path without
// credentials is challenged even while anonymous access is on: every
// registry path but the token endpoint, from which an anonymous client
// fetches the anonymous token it then pulls with (decision 0031).
func ChallengesAnonymous(path string) bool {
	rest, ok := registryRest(path)
	return ok && rest != "token"
}

// registryRest returns the part of a registry path after its /v2/, and
// whether path is a registry path at all.
func registryRest(path string) (string, bool) {
	if path == "/v2" {
		return "", true
	}
	if rest, ok := strings.CutPrefix(path, "/v2/"); ok {
		return rest, true
	}
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	_, after, ok := strings.Cut(rest, "/")
	if !ok {
		return "", false
	}
	if after == "v2" {
		return "", true
	}
	rest, ok = strings.CutPrefix(after, "v2/")
	if _, jfrog := jfrogOperations[rest]; jfrog {
		return "", false
	}
	return rest, ok
}

// jfrogOperations are the JFrog REST operations under a repository's
// /artifactory/api/docker/<repo>/v2/, with their methods: the generated
// server answers them, in the Artifactory envelope (decision 0031), so
// they are no registry paths.
var jfrogOperations = map[string]string{
	"promote":         http.MethodPost,
	"parentManifests": http.MethodPost,
	"delete":          http.MethodDelete,
}
