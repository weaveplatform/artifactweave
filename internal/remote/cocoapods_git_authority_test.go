package remote

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccCocoaPodsGitTransportCredentialIsolation(t *testing.T) {
	rootHeaders := make(chan http.Header, 2)
	childHeaders := make(chan http.Header, 2)
	serve := func(headers chan<- http.Header) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			headers <- r.Header.Clone()
			_, _ = io.WriteString(w, "git response")
		}
	}
	root := httptest.NewTLSServer(serve(rootHeaders))
	t.Cleanup(root.Close)
	child := httptest.NewTLSServer(serve(childHeaders))
	t.Cleanup(child.Close)
	var roots []byte
	for _, server := range []*httptest.Server{root, child} {
		roots = append(roots, pem.EncodeToMemory(&pem.Block{
			Type: "CERTIFICATE", Bytes: server.Certificate().Raw,
		})...)
	}
	c := repo.Config{
		Key:                         "git-auth",
		Class:                       repo.Remote,
		PackageType:                 repo.CocoaPods,
		URL:                         root.URL,
		Username:                    "source-user",
		Password:                    "source-password",
		AllowAnyHostAuth:            ptr(true),
		EnableCookieManagement:      ptr(true),
		ExternalDependenciesEnabled: ptr(true),
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Source-Secret", Value: "source-header"},
		},
		ExternalDependenciesPatterns: []string{root.URL + "/main.git", child.URL + "/dep.git"},
	}
	db := inmem.New()
	saveRepo(t, db, c)
	resolver := resolverFor(t, db, ResolverOptions{
		RootCAsPEM: roots, AllowedPrivateOrigins: []string{child.URL},
	})
	opts := resolveFor(t, resolver, c)
	childURL, err := url.Parse(child.URL)
	if err != nil {
		t.Fatal(err)
	}
	opts.Jar.SetCookies(childURL, []*http.Cookie{{Name: "session", Value: "source-cookie"}})
	client, err := NewClient(c, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	checkout := &cocoaPodsCheckout{
		policy: &Policy{}, client: client, request: source.Request{Repo: repo.Masked(c)},
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, raw := range []string{root.URL + "/main.git", child.URL + "/dep.git"} {
			response, err := cocoaPodsAuthorityExchange(t, checkout, method, raw)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
		}
		rootHeader, childHeader := <-rootHeaders, <-childHeaders
		authRequest := &http.Request{Header: rootHeader}
		user, password, ok := authRequest.BasicAuth()
		if !ok || user != c.Username || password != c.Password ||
			rootHeader.Get("X-Source-Secret") != "source-header" {
			t.Fatalf("%s root request lost configured authentication", method)
		}
		for _, name := range []string{"Authorization", "X-Source-Secret", "Cookie"} {
			if childHeader.Get(name) != "" {
				t.Fatalf("%s child request received %s", method, name)
			}
		}
		if method == http.MethodPost &&
			childHeader.Get("Content-Type") != "application/x-git-upload-pack-request" {
			t.Fatal("credential filtering removed the Git protocol content type")
		}
	}
}

func cocoaPodsAuthorityExchange(
	t *testing.T,
	checkout *cocoaPodsCheckout,
	method, repository string,
) (*http.Response, error) {
	t.Helper()
	target, body := repository+"/info/refs?service=git-upload-pack", ""
	if method == http.MethodPost {
		target, body = repository+"/git-upload-pack", "0009done\n"
	}
	request, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	transport := cocoaPodsGitTransport{checkout: checkout, repository: repository}
	response, err := transport.RoundTrip(request)
	if err != nil {
		return nil, fmt.Errorf("exchange Git fixture: %w", err)
	}
	return response, nil
}

func TestAccCocoaPodsGitDeniedSubmoduleDoesNotContactUpstream(t *testing.T) {
	w, q := cocoaPodsAuthorityFixture(t)
	w.c.ExternalDependenciesPatterns = []string{w.up.URL + "/main.git"}
	if err := w.db.Repos().Update(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	w.permit.Identity = cocoaPodsAuthorityIdentity(t, w)
	q.Repo = w.c
	if _, err := w.read(q); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("denied submodule returned %v", err)
	}
	if w.up.Count(http.MethodPost, "/main.git/git-upload-pack") != 1 ||
		w.up.Count(http.MethodGet, "/dep.git/info/refs") != 0 ||
		w.up.Count(http.MethodPost, "/dep.git/git-upload-pack") != 0 {
		t.Fatal("submodule policy was not checked before contacting the child")
	}
	cocoaPodsAuthorityNoPublication(t, w, q)
}

func TestAccCocoaPodsGitRevocationDuringSubmoduleFetch(t *testing.T) {
	for _, permission := range []bool{true, false} {
		for _, stage := range []string{"refs", "pack"} {
			name := fmt.Sprintf("permission=%t/%s", permission, stage)
			t.Run(name, func(t *testing.T) {
				w, q := cocoaPodsAuthorityFixture(t)
				barrier := new(fakeupstream.Barrier)
				t.Cleanup(barrier.Release)
				cocoaPodsAuthorityUpstream(t, w.up, stage, barrier)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					out, err := w.p.Read(ctx, q, w.permit)
					if out.Stream != nil {
						_ = out.Stream.Body.Close()
					}
					done <- err
				}()
				select {
				case <-barrier.Reached():
				case err := <-done:
					t.Fatalf("fetch ended before child barrier: %v", err)
				case <-ctx.Done():
					t.Fatal("child fetch did not reach barrier")
				}
				want := source.ErrSuperseded
				if permission {
					want = source.ErrDenied
					if err := w.db.Permissions().Delete(t.Context(), "git-reader"); err != nil {
						t.Fatal(err)
					}
				} else {
					changed := w.c
					changed.ExternalDependenciesPatterns = []string{w.up.URL + "/main.git"}
					if err := w.db.Repos().Update(t.Context(), changed); err != nil {
						t.Fatal(err)
					}
				}
				barrier.Release()
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("revoked fetch returned %v; want %v", err, want)
					}
				case <-ctx.Done():
					t.Fatal("revoked fetch did not finish")
				}
				if stage == "refs" && w.up.Count(http.MethodPost, "/dep.git/git-upload-pack") != 0 {
					t.Fatal("child upload-pack started after revocation")
				}
				cocoaPodsAuthorityNoPublication(t, w, q)
			})
		}
	}
}

func cocoaPodsAuthorityFixture(t *testing.T) (*policyWorld, source.Request) {
	t.Helper()
	w := policyFixture(t)
	w.c.PackageType = repo.CocoaPods
	w.c.ExternalDependenciesEnabled = ptr(true)
	w.c.ExternalDependenciesPatterns = []string{w.up.URL + "/**"}
	if err := w.db.Repos().Update(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	w.permit.Identity = cocoaPodsAuthorityIdentity(t, w)
	cocoaPodsAuthorityUpstream(t, w.up, "", nil)
	git := source.CocoaPodsGitSource{URL: w.up.URL + "/main.git", Branch: "main", Submodules: true}
	q := w.request("HelloPod/1.0.0/source.tar.gz", source.ResourceKindArtifact)
	q.Repo = w.c
	q.Subject = authz.Subject{Name: "git-reader"}
	q.Resource.CocoaPodsGit = &git
	q.Resource.ExternalURL, q.Resource.Variant = git.URL, git.Variant()
	q.Resource.Mutable, q.Resource.NoCrossOriginAuth = true, true
	q.Resource.CheckURL = func(context.Context, *url.URL) error { return nil }
	q.Resource.Validate = func(_ context.Context, body io.ReadSeeker, size int64) error {
		return podfmt.ValidateSourceArchive(body, size, ".tar.gz")
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "git-reader", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Targets: map[string]types.Target{w.c.Key: {}},
				Actions: types.Grants{
					Users: map[string]types.Actions{"git-reader": types.ActionRead},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	resolver := resolverFor(t, w.db, ResolverOptions{})
	w.p.Options = resolver.Options
	w.p.CheckCurrent = func(ctx context.Context, st store.Store, q source.Request, _ source.Permit, revision string) error {
		current, err := st.Repos().Get(ctx, q.Repo.Key)
		if err != nil || store.RemotePolicyChanged(current, q.Repo) {
			return source.ErrSuperseded
		}
		opts, err := resolver.Options(ctx, q.Repo)
		if err != nil || opts.Revision != revision {
			return source.ErrSuperseded
		}
		access, err := authz.New(
			ctx,
			authz.Sources{Permissions: st.Permissions(), Groups: st.Groups()},
			q.Subject,
		)
		if err != nil || !access.Allows(
			types.ResourceArtifact, authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)},
			q.Resource.Path, types.ActionRead,
		) {
			return source.ErrDenied
		}
		return nil
	}
	return w, q
}

func cocoaPodsAuthorityIdentity(t *testing.T, w *policyWorld) types.SourceIdentity {
	t.Helper()
	identity, err := w.db.RemoteCache().Source(t.Context(), w.c.Key)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func cocoaPodsAuthorityUpstream(
	t *testing.T,
	up *fakeupstream.Server,
	stage string,
	barrier *fakeupstream.Barrier,
) {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/cocoapods-git/refs.json")
	if err != nil {
		t.Fatal(err)
	}
	var refs map[string]string
	if err := json.Unmarshal(data, &refs); err != nil {
		t.Fatal(err)
	}
	packet := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	for _, fixture := range []struct{ name, path, hash string }{
		{"root", "main", refs["commit"]}, {"submodule", "dep", refs["submodule"]},
	} {
		advertised := packet("# service=git-upload-pack\n") + "0000" +
			packet(fixture.hash+" HEAD\x00ofs-delta\n") +
			packet(fixture.hash+" refs/heads/main\n") + "0000"
		pack, err := os.ReadFile("../../test/fixtures/cocoapods-git/" + fixture.name + ".pack")
		if err != nil {
			t.Fatal(err)
		}
		advert := fakeupstream.Resource{Body: []byte(advertised), Header: http.Header{
			"Content-Type": {"application/x-git-upload-pack-advertisement"},
		}}
		objects := fakeupstream.Resource{
			Method: http.MethodPost,
			Status: http.StatusOK,
			Body:   append([]byte("0008NAK\n"), pack...),
			Header: http.Header{"Content-Type": {"application/x-git-upload-pack-result"}},
		}
		if fixture.path == "dep" {
			if stage == "refs" {
				advert.Fault.BeforeBody = barrier
			} else if stage == "pack" {
				objects.Fault.BeforeBody = barrier
			}
		}
		up.Set("/"+fixture.path+".git/info/refs", advert)
		up.Set("/"+fixture.path+".git/git-upload-pack", objects)
	}
}

func cocoaPodsAuthorityNoPublication(t *testing.T, w *policyWorld, q source.Request) {
	t.Helper()
	if _, err := w.db.Items().
		Get(t.Context(), q.Repo.Key, true, q.Resource.Path); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("denied Git archive was published: %v", err)
	}
	entries, _, err := w.db.RemoteCache().List(t.Context(), q.Repo.Key, "", store.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Begin reserves a cache generation before fetching. An empty reservation
	// may remain after denial, but it must contain neither usable bytes nor a
	// positive/negative result that later readers could serve.
	for _, entry := range entries {
		if entry.ItemID != 0 || entry.Blob.Checksums.SHA256 != "" ||
			!entry.FetchedAt.IsZero() || !entry.ValidatedAt.IsZero() ||
			!entry.NegativeUntil.IsZero() {
			t.Fatalf("denied Git archive left a published cache result: %+v", entry)
		}
	}
}
