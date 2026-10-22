package remote

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

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	ErrRegistryChallenge = errors.New("invalid upstream registry authentication challenge")
	ErrRegistryDenied    = errors.New("upstream registry authentication denied")
)

// RegistryOptions comes from Resolver.RegistryOptions, never downstream headers.
// Now is an optional clock for deterministic expiry tests.
type RegistryOptions struct {
	Transport             Options
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
		return RegistryOptions{}, ErrConfiguration
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
	client        *Client
	base          *url.URL
	project       string
	tokensEnabled bool
	now           func() time.Time
	mu            sync.Mutex
	challenges    map[string]registryChallenge
	tokens        map[string]registryToken
	flights       flightGroup[registryToken]
	capacity      admission
}

func NewRegistry(c repo.Config, opts RegistryOptions) (*Registry, error) {
	if c.Class != repo.Remote ||
		(c.PackageType != repo.Docker && c.PackageType != repo.OCI && c.PackageType != repo.HelmOCI) ||
		opts.Transport.Prepare == nil ||
		opts.Transport.CheckURL == nil ||
		opts.Transport.Revision == "" ||
		c.DockerAPIVersion != "" && c.DockerAPIVersion != repo.DockerV2 {
		return nil, ErrConfiguration
	}
	base, err := checkedURL(c.URL)
	if err != nil {
		return nil, err
	}
	if c.DockerProjectID != "" && !registryName.MatchString(c.DockerProjectID) {
		return nil, ErrConfiguration
	}
	registry := &Registry{
		base:          base,
		project:       c.DockerProjectID,
		tokensEnabled: enabled(c.EnableTokenAuthentication),
		now:           opts.Now,
		challenges:    make(map[string]registryChallenge),
		tokens:        make(map[string]registryToken),
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
			return err
		}
		// This deletes configured ordinary-HTTP Bearer auth as well as any caller
		// credentials. A registry token never gains AllowAnyHostAuth's wider scope.
		req.Header.Del("Authorization")
		auth, _ := req.Context().Value(registryAuthorizationKey{}).(registryAuthorization)
		if auth.realm != "" {
			if origin(req.URL) == auth.realm &&
				r.realmCredentials(req.URL, opts.allowRealmCredentials) {
				setRegistryBasic(req, opts)
			}
		} else if origin(req.URL) == origin(r.base) {
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
	if origin(u) == origin(r.base) {
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
		return "", ErrRequest
	}
	if r.project != "" {
		name = r.project + "/" + name
	}
	if r.base.Host == "registry-1.docker.io" && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	if len(name) > 255 {
		return "", ErrRequest
	}
	return name, nil
}

// Probe performs /v2/ discovery through the same bounded challenge flow.
func (r *Registry) Probe(ctx context.Context) (*http.Response, error) {
	return r.do(ctx, "", Request{Method: http.MethodGet, Path: "v2/", Metadata: true})
}

// Do addresses a registry resource relative to an image, for example
// Do(ctx, "team/image", Request{Method: "GET", Path: "manifests/latest"}).
// A request retries authentication at most once. All returned bodies are owned
// by the caller, including terminal non-authentication error statuses.
func (r *Registry) Do(ctx context.Context, image string, q Request) (*http.Response, error) {
	image, err := r.Image(image)
	if err != nil {
		return nil, err
	}
	if q.ExternalURL != "" || strings.HasPrefix(q.Path, "/") {
		return nil, ErrRequest
	}
	q.Path = "v2/" + image + "/" + q.Path
	return r.do(ctx, image, q)
}

func (r *Registry) do(ctx context.Context, image string, q Request) (*http.Response, error) {
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
	sameOrigin := origin(res.Request.URL) == origin(r.base)
	header := res.Header.Values("WWW-Authenticate")
	_ = res.Body.Close()
	if !sameOrigin {
		return nil, ErrRegistryDenied
	}
	next, err := parseRegistryChallenge(header, image, r.tokensEnabled)
	if err != nil {
		return nil, err
	}
	// Basic was already attempted with the configured credentials.
	if next.scheme == "basic" {
		return nil, ErrRegistryDenied
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
		return nil, ErrRegistryDenied
	}
	return res, nil
}

func (r *Registry) request(
	ctx context.Context,
	image string,
	q Request,
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
	u, err := checkedURL(raw)
	if err != nil || (r.base.Scheme == "https" && u.Scheme != "https") {
		return ErrSecurity
	}
	// HTTP is accepted only at the configured registry origin, never a second
	// realm where the token or Basic credentials would travel in plaintext.
	if u.Scheme != "https" && origin(u) != origin(r.base) {
		return ErrSecurity
	}
	if err = r.client.checkURL(ctx, u); err != nil {
		return safeError(err)
	}
	return nil
}

func (r *Registry) Close() { r.client.Close() }

type pooledRegistry struct {
	fingerprint [32]byte
	registry    *Registry
}

// RegistryPool owns token and connection lifetimes. A changed repository
// incarnation, source generation or credential revision starts an empty cache.
type RegistryPool struct {
	mu      sync.Mutex
	clients map[string]pooledRegistry
	closed  bool
}

func (p *RegistryPool) Client(
	c repo.Config,
	identity types.SourceIdentity,
	opts RegistryOptions,
) (*Registry, error) {
	if identity.Kind != types.ExternalHTTP || identity.Repository == "" ||
		identity.Generation == "" {
		return nil, ErrConfiguration
	}
	encoded, err := json.Marshal([]any{c, identity, opts.Transport.Revision})
	if err != nil {
		return nil, ErrConfiguration
	}
	fingerprint := sha256.Sum256(encoded)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrClosed
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

func (p *RegistryPool) Remove(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.clients[key]; ok {
		old.registry.Close()
		delete(p.clients, key)
	}
}

func (p *RegistryPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for key, old := range p.clients {
		old.registry.Close()
		delete(p.clients, key)
	}
}
