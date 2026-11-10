package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
)

// ErrRegistryChallenge reports an upstream registry's authentication
// challenge that the registry client cannot answer: a malformed
// WWW-Authenticate header, or a Bearer challenge to a repository without
// token authentication. Its text keeps the remote: prefix of the
// transport's other sentinels.
var ErrRegistryChallenge = errors.New("remote: invalid upstream registry authentication challenge")

// RegistryOptions comes from Resolver.RegistryOptions, never downstream headers.
// Now is an optional clock for deterministic expiry tests.
type RegistryOptions struct {
	Transport             remote.Options
	Now                   func() time.Time
	username, password    string
	allowRealmCredentials bool
}

// RegistryOptions preserves the shared destination, proxy and TLS policy while
// keeping registry Basic credentials separate from ordinary HTTP Bearer mode.
func (r *Resolver) RegistryOptions(ctx context.Context, c repo.Config) (RegistryOptions, error) {
	opts, err := r.Options(ctx, c)
	if err != nil {
		return RegistryOptions{}, err
	}
	stored, err := r.store.Repos().Get(ctx, c.Key)
	if err != nil {
		return RegistryOptions{}, configurationError(err)
	}
	// The second read cannot pair new credentials with an old source revision.
	current, err := r.Options(ctx, stored)
	if err != nil || opts.Revision != current.Revision {
		return RegistryOptions{}, remote.ErrConfiguration
	}
	return RegistryOptions{
		Transport: opts, username: stored.Username, password: stored.Password,
		allowRealmCredentials: enabled(stored.AllowAnyHostAuth),
	}, nil
}

type (
	registryAuthorization    struct{ token, realm, image string }
	registryAuthorizationKey struct{}
	registryToken            struct {
		value   string
		expires time.Time
	}
)

// Registry authenticates read-only Distribution v2 requests. It is bound to one
// resolved repository revision; RegistryPool also fences its source incarnation.
// Native adapters own authorization, cache publication and digest validation.
type Registry struct {
	client      *Client
	base        *url.URL
	project     string
	allowTokens bool
	now         func() time.Time
	mu          sync.Mutex
	challenges  map[string]registryChallenge
	tokens      map[string]registryToken
	flights     remote.FlightGroup[registryToken]
	capacity    remote.Admission
}

// NewRegistry builds the registry client of remote Docker, OCI or Helm OCI
// repository c under opts, which Resolver.RegistryOptions returns: a
// Distribution v2 client whose own Prepare replaces any Authorization with
// the scoped Basic or Bearer exchange, and which keeps no cookies. A
// repository of another class or type, options without Prepare, CheckURL or
// a revision, an unsupported Docker API version or an invalid project ID is
// refused with remote.ErrConfiguration.
func NewRegistry(c repo.Config, opts RegistryOptions) (*Registry, error) {
	if c.Class != repo.Remote ||
		(c.PackageType != repo.Docker && c.PackageType != repo.OCI && c.PackageType != repo.HelmOCI) ||
		opts.Transport.Prepare == nil ||
		opts.Transport.CheckURL == nil ||
		opts.Transport.Revision == "" ||
		c.DockerAPIVersion != "" && c.DockerAPIVersion != repo.DockerV2 {
		return nil, remote.ErrConfiguration
	}
	base, err := remote.CheckedURL(c.URL)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return nil, err
	}
	if c.DockerProjectID != "" && !registryName.MatchString(c.DockerProjectID) {
		return nil, remote.ErrConfiguration
	}
	registry := &Registry{
		base:        base,
		project:     c.DockerProjectID,
		allowTokens: enabled(c.EnableTokenAuthentication),
		now:         opts.Now,
		challenges:  make(map[string]registryChallenge),
		tokens:      make(map[string]registryToken),
	}
	if registry.now == nil {
		registry.now = time.Now
	}
	connection := opts.Transport
	// Native query parameters are adapter-selected protocol operations (token
	// scopes and pagination), never arbitrary downstream query propagation.
	propagate := true
	c.PropagateQueryParams = &propagate
	connection.Prepare = registry.prepare(opts)
	// Cookies learned from the registry must not become token-server credentials.
	// Registry authentication uses only the explicitly scoped Basic/Bearer flow.
	connection.Jar = nil
	registry.client, err = NewClient(c, connection)
	if err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) prepare(opts RegistryOptions) func(*http.Request) error {
	return func(req *http.Request) error {
		if err := opts.Transport.Prepare(req); err != nil {
			//nolint:wrapcheck // preserves upstream error identity
			return err
		}
		// This deletes configured ordinary-HTTP Bearer auth as well as any caller
		// credentials. A registry token never gains AllowAnyHostAuth's wider scope.
		req.Header.Del("Authorization")
		auth, _ := req.Context().Value(registryAuthorizationKey{}).(registryAuthorization)
		if auth.realm != "" {
			if remote.Origin(req.URL) == auth.realm &&
				r.realmCredentials(req.URL, opts.allowRealmCredentials) {
				setRegistryBasic(req, opts)
			}
		} else if remote.Origin(req.URL) == remote.Origin(r.base) {
			if auth.token != "" {
				prefix := strings.TrimRight(r.base.Path, "/") + "/v2/"
				permitted := req.URL.Path == prefix
				if auth.image != "" {
					permitted = strings.HasPrefix(req.URL.Path, prefix+auth.image+"/")
				}
				if permitted {
					req.Header.Set("Authorization", "Bearer "+auth.token)
				}
			} else {
				setRegistryBasic(req, opts)
			}
		}
		return nil
	}
}

func setRegistryBasic(req *http.Request, opts RegistryOptions) {
	if opts.username != "" || opts.password != "" {
		req.SetBasicAuth(opts.username, opts.password)
	}
}

func (r *Registry) realmCredentials(u *url.URL, allow bool) bool {
	if remote.Origin(u) == remote.Origin(r.base) {
		return true
	}
	if u.Scheme != "https" {
		return false
	}
	// Docker's documented realm is a narrow trust relationship, not a suffix or
	// a redirect grant. Other public realms are anonymous unless explicitly opted in.
	hub := r.base.Scheme == "https" && r.base.Host == "registry-1.docker.io" &&
		u.Host == "auth.docker.io" && u.Path == "/token"
	return hub || allow
}

var registryName = regexp.MustCompile(
	`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`,
)

// Image returns the upstream name used in both the URL and the pull scope.
// dockerProjectId is an explicit namespace prefix. Docker Hub shorthand gets
// its protocol-defined library namespace; other registries retain the name.
func (r *Registry) Image(name string) (string, error) {
	if len(name) > 255 || !registryName.MatchString(name) {
		return "", remote.ErrRequest
	}
	if r.project != "" {
		name = r.project + "/" + name
	}
	if r.base.Host == "registry-1.docker.io" && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	if len(name) > 255 {
		return "", remote.ErrRequest
	}
	return name, nil
}

// Probe performs /v2/ discovery through the same bounded challenge flow.
func (r *Registry) Probe(ctx context.Context) (*http.Response, error) {
	return r.do(ctx, "", remote.Request{Method: http.MethodGet, Path: "v2/", Metadata: true})
}

// Do addresses a registry resource relative to an image, for example
// Do(ctx, "team/image", Request{Method: "GET", Path: "manifests/latest"}).
// A request retries authentication at most once. All returned bodies are owned
// by the caller, including terminal non-authentication error statuses.
func (r *Registry) Do(ctx context.Context, image string, q remote.Request) (*http.Response, error) {
	image, err := r.Image(image)
	if err != nil {
		return nil, err
	}
	if q.ExternalURL != "" || strings.HasPrefix(q.Path, "/") {
		return nil, remote.ErrRequest
	}
	q.Path = "v2/" + image + "/" + q.Path
	return r.do(ctx, image, q)
}

func (r *Registry) do(ctx context.Context, image string, q remote.Request) (*http.Response, error) {
	challenge, token := r.cached(image)
	var err error
	if challenge.scheme == "bearer" && token.value == "" {
		token, err = r.token(ctx, challenge)
		if err != nil {
			return nil, err
		}
	}
	res, err := r.request(ctx, image, q, token.value)
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		return res, err
	}
	// Never trust an authentication challenge served by a blob/CDN redirect.
	sameOrigin := remote.Origin(res.Request.URL) == remote.Origin(r.base)
	header := res.Header.Values("WWW-Authenticate")
	_ = res.Body.Close()
	if !sameOrigin {
		return nil, remote.ErrRegistryDenied
	}
	next, err := parseRegistryChallenge(header, image, r.allowTokens)
	if err != nil {
		return nil, err
	}
	// Basic was already attempted with the configured credentials.
	if next.scheme == "basic" {
		return nil, remote.ErrRegistryDenied
	}
	if err = r.checkRealm(ctx, next.realm); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.tokens[challenge.key()].value == token.value {
		delete(r.tokens, challenge.key())
	}
	if len(r.challenges) >= 256 {
		clear(r.challenges)
	}
	r.challenges[image] = next
	r.mu.Unlock()
	token, err = r.token(ctx, next)
	if err != nil {
		return nil, err
	}
	res, err = r.request(ctx, image, q, token.value)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		_ = res.Body.Close()
		r.mu.Lock()
		if r.tokens[next.key()].value == token.value {
			delete(r.tokens, next.key())
		}
		r.mu.Unlock()
		return nil, remote.ErrRegistryDenied
	}
	return res, nil
}

func (r *Registry) request(
	ctx context.Context,
	image string,
	q remote.Request,
	token string,
) (*http.Response, error) {
	ctx = context.WithValue(
		ctx,
		registryAuthorizationKey{},
		registryAuthorization{token: token, image: image},
	)
	return r.client.Do(ctx, q)
}

func (r *Registry) cached(image string) (registryChallenge, registryToken) {
	r.mu.Lock()
	defer r.mu.Unlock()
	challenge := r.challenges[image]
	token := r.tokens[challenge.key()]
	if !token.expires.After(r.now()) {
		token = registryToken{}
	}
	return challenge, token
}

func (r *Registry) checkRealm(ctx context.Context, raw string) error {
	u, err := remote.CheckedURL(raw)
	if err != nil || (r.base.Scheme == "https" && u.Scheme != "https") {
		return remote.ErrSecurity
	}
	// HTTP is accepted only at the configured registry origin, never a second
	// realm where the token or Basic credentials would travel in plaintext.
	if u.Scheme != "https" && remote.Origin(u) != remote.Origin(r.base) {
		return remote.ErrSecurity
	}
	if err = r.client.checkURL(ctx, u); err != nil {
		return safeError(err)
	}
	return nil
}

// Close releases the registry client's idle connections.
func (r *Registry) Close() { r.client.Close() }

// Base returns a copy of the registry's base URL, so that no caller can
// change where the client sends its requests.
func (r *Registry) Base() *url.URL {
	u := *r.base
	return &u
}

type pooledRegistry struct {
	fingerprint [32]byte
	registry    *Registry
}

// RegistryPool owns token and connection lifetimes. A changed repository
// incarnation, source generation or credential revision starts an empty cache.
type RegistryPool struct {
	mu       sync.Mutex
	clients  map[string]pooledRegistry
	isClosed bool
}

// Client returns the pooled registry client of repository c for the source
// identity, or builds one with NewRegistry when c, the identity or the
// credential revision changed, closing the one it replaces, so its tokens
// and challenges start empty. An identity that is not a complete external
// HTTP source answers remote.ErrConfiguration, a closed pool
// remote.ErrClosed.
func (p *RegistryPool) Client(
	c repo.Config,
	identity model.SourceIdentity,
	opts RegistryOptions,
) (*Registry, error) {
	if identity.Kind != model.ExternalHTTP || identity.Repository == "" ||
		identity.Generation == "" {
		return nil, remote.ErrConfiguration
	}
	encoded, err := json.Marshal([]any{c, identity, opts.Transport.Revision})
	if err != nil {
		return nil, remote.ErrConfiguration
	}
	fingerprint := sha256.Sum256(encoded)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isClosed {
		return nil, remote.ErrClosed
	}
	if old, ok := p.clients[c.Key]; ok && old.fingerprint == fingerprint {
		return old.registry, nil
	}
	registry, err := NewRegistry(c, opts)
	if err != nil {
		return nil, err
	}
	if old, ok := p.clients[c.Key]; ok {
		old.registry.Close()
	}
	if p.clients == nil {
		p.clients = make(map[string]pooledRegistry)
	}
	p.clients[c.Key] = pooledRegistry{fingerprint, registry}
	return registry, nil
}

// Remove closes and forgets the registry client of the repository named
// key, with its tokens.
func (p *RegistryPool) Remove(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.clients[key]; ok {
		old.registry.Close()
		delete(p.clients, key)
	}
}

// RegistryClients implements Registries over the application's RegistryPool
// and the resolver's registry options (Resolver.RegistryOptions).
type RegistryClients struct {
	Pool    *RegistryPool
	Options func(context.Context, repo.Config) (RegistryOptions, error)
}

// Open resolves c's registry options, refuses with source.ErrSuperseded when
// their credential revision is not revision, the one the read was admitted
// under, and returns the pool's client for c and id. On every error it
// returns a literal nil, never a nil *Registry inside a non-nil
// RegistryClient, which a caller that checks the client for nil would go on
// to use.
func (r *RegistryClients) Open(
	ctx context.Context,
	c repo.Config,
	id model.SourceIdentity,
	revision string,
) (remote.RegistryClient, error) {
	creds, err := r.Options(ctx, c)
	if err != nil {
		return nil, err
	}
	if creds.Transport.Revision != revision {
		return nil, source.ErrSuperseded
	}
	client, err := r.Pool.Client(c, id, creds)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Foreign sends q once through a client built from opts for c, and closes
// it, for a foreign layer a manifest names (decision 0031). It is unpooled on
// purpose: the pool's fingerprint covers the repository, the source identity
// and the credential revision but not opts.Prepare and opts.CheckRedirect,
// which the caller sets to strip registry credentials and custom headers from
// a foreign layer's request, so a pooled client could send them (decision
// 0007).
func (r *RegistryClients) Foreign(
	ctx context.Context,
	c repo.Config,
	opts remote.Options,
	q remote.Request,
) (*http.Response, error) {
	client, err := NewClient(c, opts)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.Do(ctx, q)
}

// Close closes every pooled registry client, for application shutdown;
// every later Client answers remote.ErrClosed.
func (p *RegistryPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isClosed = true
	for key, old := range p.clients {
		old.registry.Close()
		delete(p.clients, key)
	}
}
