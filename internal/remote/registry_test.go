package remote

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type registryFixture struct {
	t            *testing.T
	router       *chi.Mux
	server       *httptest.Server
	registry     *Registry
	config       repo.Config
	options      RegistryOptions
	exchanges    atomic.Int32
	pulls        atomic.Int32
	tokensMu     sync.Mutex
	scopes       []string
	service      string
	tokenHandler http.HandlerFunc
	pullHandler  http.HandlerFunc
	now          atomic.Int64
}

func newRegistryFixture(t *testing.T, user string) *registryFixture {
	t.Helper()
	f := &registryFixture{t: t, router: chi.NewRouter(), service: "registry.example"}
	f.now.Store(time.Now().Unix())
	f.router.Get("/token", func(w http.ResponseWriter, r *http.Request) {
		f.exchanges.Add(1)
		f.tokensMu.Lock()
		f.scopes = append(f.scopes, r.URL.Query().Get("scope"))
		f.tokensMu.Unlock()
		if r.URL.Query().Get("service") != f.service {
			t.Error("token service changed")
		}
		username, password, basic := r.BasicAuth()
		if user != "" && (!basic || username != user || password != "upstream-password") {
			t.Error("upstream Basic credentials missing")
		}
		if user == "" && r.Header.Get("Authorization") != "" {
			t.Error("anonymous token request received credentials")
		}
		if strings.Contains(r.Header.Get("Authorization"), "local-artifactweave") ||
			r.Header.Get("Cookie") != "" ||
			r.Header.Get("X-Jfrog-Art-Api") != "" {
			t.Error("downstream secret forwarded")
		}
		if f.tokenHandler != nil {
			f.tokenHandler(w, r)
			return
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"token": "upstream-token-" + strings.ReplaceAll(r.URL.Query().Get("scope"), ":", "."), "expires_in": 60})
	})
	handler := func(w http.ResponseWriter, r *http.Request) {
		f.pulls.Add(1)
		if f.pullHandler != nil {
			f.pullHandler(w, r)
			return
		}
		image := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/manifests/latest")
		if r.URL.Path == "/v2/" {
			image = ""
		}
		scope := ""
		if image != "" {
			scope = "repository:" + image + ":pull"
		}
		want := "Bearer upstream-token-" + strings.ReplaceAll(scope, ":", ".")
		if r.Header.Get("Authorization") != want {
			w.Header().
				Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q,scope=%q`, f.server.URL+"/token", f.service, scope))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "registry-content")
	}
	f.router.Get("/v2/", handler)
	f.router.Get("/v2/*", handler)
	f.server = httptest.NewServer(f.router)
	t.Cleanup(f.server.Close)
	f.config = repo.Config{
		Key: "docker-remote", Class: repo.Remote, PackageType: "docker", URL: f.server.URL,
		EnableTokenAuthentication: ptr(true), Username: user,
	}
	if user != "" {
		f.config.Password = "upstream-password"
	}
	st := inmem.New()
	saveRepo(t, st, f.config)
	var err error
	f.options, err = resolverFor(
		t,
		st,
		ResolverOptions{},
	).RegistryOptions(t.Context(), repo.Masked(f.config))
	if err != nil {
		t.Fatal(err)
	}
	f.options.Now = func() time.Time { return time.Unix(f.now.Load(), 0) }
	f.registry, err = NewRegistry(f.config, f.options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.registry.Close)
	return f
}

func registryGet(ctx context.Context, registry *Registry, image string) error {
	res, err := registry.Do(
		ctx,
		image,
		Request{Method: http.MethodGet, Path: "manifests/latest", Header: http.Header{
			"Authorization": {
				"Bearer local-artifactweave-token",
			},
			"Cookie":          {"local=secret"},
			"X-Jfrog-Art-Api": {"local-artifactweave-key"},
		}},
	)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read registry body: %w", err)
	}
	if res.StatusCode != http.StatusOK || string(body) != "registry-content" {
		return fmt.Errorf("unexpected registry response: %d", res.StatusCode)
	}
	return nil
}

func TestAccRegistryAuthenticationAndExpiry(t *testing.T) {
	for _, user := range []string{"", "reader"} {
		t.Run("user="+user, func(t *testing.T) {
			f := newRegistryFixture(t, user)
			probe, err := f.registry.Probe(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			probe.Body.Close()
			for range 2 {
				if err := registryGet(t.Context(), f.registry, "team/image"); err != nil {
					t.Fatal(err)
				}
			}
			if f.exchanges.Load() != 2 {
				t.Fatalf("probe plus one scoped exchange: %d", f.exchanges.Load())
			}
			if err := registryGet(t.Context(), f.registry, "other/image"); err != nil {
				t.Fatal(err)
			}
			if f.exchanges.Load() != 3 {
				t.Fatal("token leaked across images")
			}
			f.now.Add(61)
			if err := registryGet(t.Context(), f.registry, "team/image"); err != nil {
				t.Fatal(err)
			}
			if f.exchanges.Load() != 4 {
				t.Fatal("expired token not refreshed")
			}
			f.tokensMu.Lock()
			got := strings.Join(f.scopes, "|")
			f.tokensMu.Unlock()
			if got != "|repository:team/image:pull|repository:other/image:pull|repository:team/image:pull" {
				t.Fatal(got)
			}
		})
	}
}

func TestAccRegistryDeniedAndBoundedRetry(t *testing.T) {
	t.Run("token rejected", func(t *testing.T) {
		f := newRegistryFixture(t, "reader")
		f.tokenHandler = func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"token":"denied"}`) }
		if err := registryGet(
			t.Context(),
			f.registry,
			"team/image",
		); !errors.Is(
			err,
			ErrRegistryDenied,
		) {
			t.Fatal(err)
		}
		if f.exchanges.Load() != 1 || f.pulls.Load() != 2 {
			t.Fatalf("unbounded auth: %d/%d", f.exchanges.Load(), f.pulls.Load())
		}
	})
	t.Run("cached token revoked", func(t *testing.T) {
		f := newRegistryFixture(t, "")
		if err := registryGet(t.Context(), f.registry, "team/image"); err != nil {
			t.Fatal(err)
		}
		f.tokenHandler = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }
		f.pullHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().
				Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q,scope="repository:team/image:pull"`, f.server.URL+"/token", f.service))
			w.WriteHeader(http.StatusUnauthorized)
		}
		if err := registryGet(
			t.Context(),
			f.registry,
			"team/image",
		); !errors.Is(
			err,
			ErrRegistryDenied,
		) {
			t.Fatal(err)
		}
		if f.exchanges.Load() != 2 {
			t.Fatal("revoked token not refreshed once")
		}
	})
	for _, tc := range []struct {
		name   string
		status int
	}{{"denied", 401}, {"forbidden", 403}, {"limited", 429}, {"unavailable", 503}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRegistryFixture(t, "")
			f.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "99999")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "upstream-secret")
			}
			err := registryGet(t.Context(), f.registry, "team/image")
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
			if tc.status < 429 && !errors.Is(err, ErrRegistryDenied) {
				t.Fatal(err)
			}
			if tc.status >= 429 {
				var failure *source.HTTPError
				if !errors.As(err, &failure) || failure.Status != tc.status {
					t.Fatal(err)
				}
				if tc.status == 429 && failure.RetryAfter != 300*time.Second {
					t.Fatal(failure.RetryAfter)
				}
			}
			if f.exchanges.Load() != 1 || f.pulls.Load() != 1 {
				t.Fatal("retried a denied exchange")
			}
		})
	}
}

func TestAccRegistryExchangeCoalescingAndCancellation(t *testing.T) {
	f := newRegistryFixture(t, "")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"token":"upstream-token-repository.team/image.pull"}`)
	}
	firstCtx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { first <- registryGet(firstCtx, f.registry, "team/image") }()
	<-entered
	var group sync.WaitGroup
	failures := make(chan error, 16)
	for range 16 {
		group.Go(func() { failures <- registryGet(t.Context(), f.registry, "team/image") })
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.registry.flights.mu.Lock()
		count := 0
		for _, active := range f.registry.flights.active {
			count = active.waiters
		}
		f.registry.flights.mu.Unlock()
		if count == 17 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiters did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.exchanges.Load() != 1 {
		t.Fatal("scope exchange not coalesced")
	}
}

func TestAccRegistryBasicAndDisabledBearer(t *testing.T) {
	f := newRegistryFixture(t, "reader")
	f.pullHandler = func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "reader" || password != "upstream-password" {
			t.Error("Basic not supplied")
		}
		_, _ = io.WriteString(w, "registry-content")
	}
	if err := registryGet(t.Context(), f.registry, "image"); err != nil {
		t.Fatal(err)
	}
	f.pullHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		w.WriteHeader(401)
	}
	if err := registryGet(t.Context(), f.registry, "image"); !errors.Is(err, ErrRegistryDenied) {
		t.Fatal(err)
	}
	f.config.EnableTokenAuthentication = ptr(false)
	disabled, err := NewRegistry(f.config, f.options)
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	f.pullHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, f.server.URL+"/token"))
		w.WriteHeader(401)
	}
	if err := registryGet(t.Context(), disabled, "image"); !errors.Is(err, ErrRegistryChallenge) {
		t.Fatal(err)
	}
	if f.exchanges.Load() != 0 {
		t.Fatal("disabled token exchange ran")
	}
}

func TestAccRegistryRealmAndRedirectIsolation(t *testing.T) {
	f := newRegistryFixture(t, "reader")
	for _, realm := range []string{"http://169.254.169.254/latest/meta-data", "http://127.0.0.1:1/token", "file:///secrets", "https://user:password@example.com/token", "https://metadata.google.internal/token"} {
		t.Run(realm, func(t *testing.T) {
			f.pullHandler = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().
					Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q`, realm, f.service))
				w.WriteHeader(401)
			}
			err := registryGet(t.Context(), f.registry, "image")
			if err == nil {
				t.Fatal("malicious realm accepted")
			}
			if strings.Contains(err.Error(), realm) {
				t.Fatal("realm leaked in error")
			}
		})
	}
	if f.exchanges.Load() != 0 {
		t.Fatal("malicious realm fetched")
	}

	var seen atomic.Int32
	cdnRouter := chi.NewRouter()
	cdnRouter.Get("/blob", func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Secret") != "" {
			t.Error("redirect leaked registry credentials")
		}
		_, _ = io.WriteString(w, "registry-content")
	})
	cdnRouter.Get("/challenge", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, f.server.URL+"/token"))
		w.WriteHeader(401)
	})
	cdn := httptest.NewServer(cdnRouter)
	defer cdn.Close()
	st := inmem.New()
	saveRepo(t, st, f.config)
	opts, err := resolverFor(
		t,
		st,
		ResolverOptions{AllowedPrivateOrigins: []string{cdn.URL}},
	).RegistryOptions(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	redirected, err := NewRegistry(f.config, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer redirected.Close()
	f.pullHandler = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, cdn.URL+"/blob", 307) }
	if err := registryGet(t.Context(), redirected, "image"); err != nil {
		t.Fatal(err)
	}
	if seen.Load() != 1 {
		t.Fatal("CDN not fetched")
	}
	f.pullHandler = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, cdn.URL+"/challenge", 307) }
	if err := registryGet(t.Context(), redirected, "image"); !errors.Is(err, ErrRegistryDenied) {
		t.Fatal(err)
	}
	if f.exchanges.Load() != 0 {
		t.Fatal("CDN challenge exchanged")
	}
}

func TestAccRegistryHTTPSRealmCredentialOptInAndRedirect(t *testing.T) {
	var authHeader string
	tokenRouter := chi.NewRouter()
	tokenRouter.Get("/token", func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"token":"upstream-token-repository.team/image.pull"}`)
	})
	token := httptest.NewTLSServer(tokenRouter)
	defer token.Close()
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			f := newRegistryFixture(t, "reader")
			f.config.AllowAnyHostAuth = ptr(allow)
			st := inmem.New()
			saveRepo(t, st, f.config)
			roots := pem.EncodeToMemory(
				&pem.Block{Type: "CERTIFICATE", Bytes: token.Certificate().Raw},
			)
			opts, err := resolverFor(
				t,
				st,
				ResolverOptions{RootCAsPEM: roots, AllowedPrivateOrigins: []string{token.URL}},
			).RegistryOptions(t.Context(), f.config)
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewRegistry(f.config, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			f.pullHandler = func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(
					"Authorization",
				) == "Bearer upstream-token-repository.team/image.pull" {
					_, _ = io.WriteString(w, "registry-content")
					return
				}
				w.Header().
					Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q,scope="repository:team/image:pull"`, token.URL+"/token", f.service))
				w.WriteHeader(401)
			}
			if err := registryGet(t.Context(), client, "team/image"); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(authHeader, "Basic ") != allow {
				t.Fatalf("realm credential opt-in mismatch %q", authHeader)
			}
		})
	}
}

func TestAccRegistryPoolAndNaming(t *testing.T) {
	f := newRegistryFixture(t, "")
	var pool RegistryPool
	identity := types.SourceIdentity{
		Kind:       types.ExternalHTTP,
		Repository: "incarnation",
		Generation: "one",
	}
	first, err := pool.Client(f.config, identity, f.options)
	if err != nil {
		t.Fatal(err)
	}
	same, err := pool.Client(f.config, identity, f.options)
	if err != nil || same != first {
		t.Fatal("pool not reused", err)
	}
	if err := registryGet(t.Context(), first, "image"); err != nil {
		t.Fatal(err)
	}
	identity.Generation = "two"
	changed, err := pool.Client(f.config, identity, f.options)
	if err != nil || changed == first {
		t.Fatal("source generation reused tokens", err)
	}
	if err := registryGet(t.Context(), changed, "image"); err != nil {
		t.Fatal(err)
	}
	f.options.Transport.Revision = "changed-credential"
	rotated, err := pool.Client(f.config, identity, f.options)
	if err != nil || rotated == changed {
		t.Fatal("credential revision reused tokens", err)
	}
	pool.Remove(f.config.Key)
	pool.Remove("missing")
	again, err := pool.Client(f.config, identity, f.options)
	if err != nil || again == rotated {
		t.Fatal("deleted repository reused tokens", err)
	}
	pool.Close()
	pool.Close()
	if _, err := pool.Client(f.config, identity, f.options); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../secret", "Team/image", "team//image", "image:tag", "team/%2e%2e", "team/image?query", strings.Repeat("a", 256)} {
		if _, err := f.registry.Image(name); err == nil {
			t.Fatal("invalid image", name)
		}
	}
	f.registry.project = "project"
	if name, err := f.registry.Image("team/image"); err != nil || name != "project/team/image" {
		t.Fatal(name, err)
	}
	f.registry.base, _ = url.Parse("https://registry-1.docker.io")
	f.registry.project = ""
	if name, err := f.registry.Image("alpine"); err != nil || name != "library/alpine" {
		t.Fatal(name, err)
	}
	for _, tc := range []struct {
		raw             string
		allow, expected bool
	}{
		{"https://auth.docker.io/token", false, true},
		{"https://auth.docker.io/other", false, false},
		{"https://auth.docker.io.evil/token", false, false},
		{"https://auth.docker.io:443/token", false, false},
		{"https://other.example/token", true, true},
		{"http://other.example/token", true, false},
	} {
		u, _ := url.Parse(tc.raw)
		if got := f.registry.realmCredentials(u, tc.allow); got != tc.expected {
			t.Fatal(tc.raw, got)
		}
	}
}

func TestAccRegistryPublicRealmDNSDenied(t *testing.T) {
	f := newRegistryFixture(t, "")
	st := inmem.New()
	saveRepo(t, st, f.config)
	opts, err := resolverFor(
		t,
		st,
		ResolverOptions{LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		}},
	).RegistryOptions(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(f.config, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if err := registry.checkRealm(
		t.Context(),
		"https://public.example/token",
	); !errors.Is(
		err,
		ErrSecurity,
	) {
		t.Fatal(err)
	}
}

func TestAccRegistryTokenNeverCrossesImageRedirect(t *testing.T) {
	f := newRegistryFixture(t, "reader")
	if err := registryGet(t.Context(), f.registry, "team/image"); err != nil {
		t.Fatal(err)
	}
	var target atomic.Int32
	f.pullHandler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/other/image/manifests/latest" {
			target.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Error("image token forwarded across image redirect")
			}
			_, _ = io.WriteString(w, "registry-content")
			return
		}
		http.Redirect(w, r, "/v2/other/image/manifests/latest", http.StatusTemporaryRedirect)
	}
	if err := registryGet(t.Context(), f.registry, "team/image"); err != nil {
		t.Fatal(err)
	}
	if target.Load() != 1 {
		t.Fatal("redirect not followed")
	}
}

func TestAccRegistryTokenResponseFaults(t *testing.T) {
	for _, kind := range []string{"oversize", "short", "cancel", "redirect-loop"} {
		t.Run(kind, func(t *testing.T) {
			f := newRegistryFixture(t, "")
			f.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "oversize":
					_, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+1))
				case "short":
					w.Header().Set("Content-Length", "100")
					_, _ = io.WriteString(w, `{"token":"incomplete`)
				case "cancel":
					<-r.Context().Done()
				case "redirect-loop":
					http.Redirect(w, r, f.server.URL+"/token?"+r.URL.RawQuery, 307)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := registryGet(ctx, f.registry, "team/image")
			if err == nil {
				t.Fatal("accepted response fault")
			}
			if kind == "cancel" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		})
	}
}

func TestAccRegistryInvalidConfigurationAndRequests(t *testing.T) {
	f := newRegistryFixture(t, "")
	for _, change := range []func(*repo.Config){
		func(c *repo.Config) { c.Class = repo.Local },
		func(c *repo.Config) { c.DockerAPIVersion = repo.DockerV1 }, func(c *repo.Config) { c.PackageType = "npm" },
		func(c *repo.Config) { c.URL = "file:///bad" }, func(c *repo.Config) { c.DockerProjectID = "../bad" },
		func(c *repo.Config) { c.LocalAddress = "not-an-address" },
	} {
		c := f.config
		change(&c)
		if _, err := NewRegistry(c, f.options); err == nil {
			t.Fatal("invalid registry created")
		}
	}
	if _, err := NewRegistry(f.config, RegistryOptions{}); err == nil {
		t.Fatal("unresolved options accepted")
	}
	if _, err := f.registry.Do(
		t.Context(),
		"bad/name:tag",
		Request{Method: "GET", Path: "manifests/latest"},
	); err == nil {
		t.Fatal("bad image")
	}
	if _, err := f.registry.Do(
		t.Context(),
		"image",
		Request{Method: "GET", ExternalURL: f.server.URL},
	); err == nil {
		t.Fatal("absolute image path")
	}
	if _, err := f.registry.Do(
		t.Context(),
		"image",
		Request{Method: "GET", Path: "/manifests/latest"},
	); err == nil {
		t.Fatal("absolute image path")
	}
	if _, err := f.registry.Do(
		t.Context(),
		"image",
		Request{Method: "POST", Path: "manifests/latest"},
	); err == nil {
		t.Fatal("write method")
	}
	f.registry.project = strings.Repeat("a", 255)
	if _, err := f.registry.Image("image"); err == nil {
		t.Fatal("oversized project image")
	}
	var pool RegistryPool
	if _, err := pool.Client(f.config, types.SourceIdentity{}, f.options); err == nil {
		t.Fatal("source identity absent")
	}
	identity := types.SourceIdentity{Kind: types.ExternalHTTP, Repository: "one", Generation: "one"}
	bad := f.config
	bad.PackageType = "npm"
	if _, err := pool.Client(bad, identity, f.options); err == nil {
		t.Fatal("invalid pooled registry")
	}
	st := inmem.New()
	if _, err := resolverFor(
		t,
		st,
		ResolverOptions{},
	).RegistryOptions(t.Context(), f.config); err == nil {
		t.Fatal("missing repository resolved")
	}
}

func TestAccRegistryProjectAndRepositoryIsolation(t *testing.T) {
	f := newRegistryFixture(t, "")
	var pool RegistryPool
	defer pool.Close()
	st := inmem.New()
	resolver := resolverFor(t, st, ResolverOptions{})
	for _, key := range []string{"left-remote", "right-remote"} {
		config := f.config
		config.Key, config.DockerProjectID = key, "gcp-project"
		saveRepo(t, st, config)
		options, err := resolver.RegistryOptions(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		client, err := pool.Client(
			config,
			types.SourceIdentity{
				Kind:       types.ExternalHTTP,
				Repository: types.RepositoryIncarnation(key),
				Generation: "one",
			},
			options,
		)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := registryGet(t.Context(), client, "team/image"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if f.exchanges.Load() != 2 {
		t.Fatal("repositories shared a token exchange")
	}
	f.tokensMu.Lock()
	defer f.tokensMu.Unlock()
	if strings.Join(
		f.scopes,
		"|",
	) != "repository:gcp-project/team/image:pull|repository:gcp-project/team/image:pull" {
		t.Fatal(f.scopes)
	}
}
