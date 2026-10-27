package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type sourceBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f sourceBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

func sourceIdentity(context.Context, repo.Config) (types.SourceIdentity, error) {
	return types.SourceIdentity{
		Kind:       types.ExternalHTTP,
		Repository: "repo-1",
		ID:         "http-1",
		Generation: "revision-1",
	}, nil
}

type sourceBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *sourceBody) Close() error { b.closes.Add(1); return nil }

func TestAccRemoteSourceRoutingAndDenials(t *testing.T) {
	var calls int
	var last source.Request
	var returned *sourceBody
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, p source.Permit) (source.Result, error) {
				calls++
				last = q
				if p.Intent != source.RetrievalIntentMayFetch || !p.UpstreamNetworkAllowed {
					t.Fatalf("wrong intent %+v", p)
				}
				returned = &sourceBody{Reader: strings.NewReader("fetched")}
				return source.Result{
					Stream: &source.Stream{
						Body:   returned,
						Status: 200,
						Representation: source.Representation{
							Size:      7,
							MediaType: "application/octet-stream",
							ETag:      `"new"`,
						},
					},
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		res := w.do(
			method,
			"/artifactory/up/a%20b.pkg?variant=x",
			"reader",
			"",
			"Authorization",
			"Basic cmVhZGVyOnB3",
			"Cookie",
			"secret=private",
		)
		if res.Code != 200 || (method == http.MethodGet && res.Body.String() != "fetched") ||
			(method == http.MethodHead && res.Body.Len() != 0) {
			t.Fatalf("%s: %d %q", method, res.Code, res.Body.String())
		}
		if returned.closes.Load() != 1 || last.Resource.Path != "a b.pkg" ||
			last.Resource.EscapedPath != "a%20b.pkg" ||
			last.Resource.Query != "variant=x" ||
			last.Resource.Method != method ||
			last.Subject.Name != "reader" {
			t.Fatalf("request/close %+v count%d", last, returned.closes.Load())
		}
	}
	if err := w.db.Repos().
		Create(t.Context(), *repo.Defaults(&repo.Config{Key: "pypi-remote", Class: repo.Remote, PackageType: repo.PyPI, URL: "https://up.example"})); err != nil {
		t.Fatal(err)
	}
	before := calls
	for _, tc := range []struct {
		method, path, user string
		status             int
	}{
		{http.MethodGet, "/artifactory/up/a.pkg", "user", 403},
		{http.MethodGet, "/artifactory/up/a.pkg", "", 401},
		{http.MethodGet, "/artifactory/missing/a.pkg", "reader", 404},
		{http.MethodGet, "/artifactory/pypi-remote/a.pom", "reader", 404},
		{http.MethodGet, "/artifactory/up/a//b", "reader", 400},
		{http.MethodGet, "/artifactory/up/a%5Cb", "reader", 400},
		{http.MethodGet, "/artifactory/up-cache/a.pkg", "reader", 404},
		{http.MethodHead, "/artifactory/up-cache/a.pkg", "reader", 404},
		{http.MethodGet, "/artifactory/virt/a.pkg", "reader", 404},
		{http.MethodPut, "/artifactory/up/a.pkg", "admin", 405},
		{http.MethodPut, "/artifactory/up-cache/a.pkg", "admin", 405},
	} {
		res := w.do(tc.method, tc.path, tc.user, "body")
		if res.Code != tc.status {
			t.Errorf("%s %s as%s: %d %s", tc.method, tc.path, tc.user, res.Code, res.Body)
		}
	}
	if calls != before {
		t.Fatalf("denied/cache/virtual/write requests fetched: %d -> %d", before, calls)
	}
	for _, config := range []repo.Config{
		{Key: "up", Class: repo.Remote, URL: "https://secret:password@up.example", BlackedOut: new(true)},
		{Key: "up", Class: repo.Remote, URL: "https://secret:password@up.example", ExcludesPattern: new("**")},
	} {
		if err := w.db.Repos().Update(t.Context(), config); err != nil {
			t.Fatal(err)
		}
		res := w.do(http.MethodGet, "/artifactory/up/a.pkg", "reader", "")
		if res.Code != 403 && res.Code != 404 {
			t.Fatalf("restriction status%d", res.Code)
		}
		if strings.Contains(res.Body.String(), "password") {
			t.Fatal("credential leak")
		}
	}
	if calls != before {
		t.Fatal("restricted request fetched")
	}
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		key := "native-" + string(class)
		if err := w.db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: key, Class: class})); err != nil {
			t.Fatal(err)
		}
		if res := w.do(
			http.MethodPut,
			"/artifactory/"+key+"/file",
			"admin",
			"original",
		); res.Code != 201 {
			t.Fatal(res.Code, res.Body)
		}
		if res := w.do(
			http.MethodGet,
			"/artifactory/"+key+"/file",
			"reader",
			"",
		); res.Code != 200 ||
			res.Body.String() != "original" {
			t.Fatal(res.Code, res.Body)
		}
	}
	if calls != before {
		t.Fatal("local/federated fetched")
	}
}

func TestAccRemoteSourceCommittedItemAndSafeErrors(t *testing.T) {
	var result source.Result
	var fail error
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) { return result, fail },
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	// The fixture caches real bytes without making client deploy to a remote legal.
	if res := w.do(
		http.MethodPut,
		"/artifactory/libs-local/a.pkg",
		"admin",
		"committed",
	); res.Code != 201 {
		t.Fatal(res.Code, res.Body)
	}
	it, err := w.db.Items().Get(t.Context(), "libs-local", false, "a.pkg")
	if err != nil {
		t.Fatal(err)
	}
	it.RepoKey, it.Cached = "up", true
	result = source.Result{Item: &it}
	res := w.do(http.MethodGet, "/artifactory/up/a.pkg", "reader", "")
	if res.Code != 200 || res.Body.String() != "committed" {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	for _, status := range []int{401, 403, 429, 503, 504} {
		result = source.Result{}
		fail = &source.HTTPError{Status: status}
		res = w.do(http.MethodGet, "/artifactory/up/a.pkg", "reader", "")
		if res.Code != status || res.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("upstream status%d: %d %+v", status, res.Code, res.Header())
		}
	}
	fail = errors.New("https://user:secret@example.com")
	res = w.do(http.MethodGet, "/artifactory/up/a.pkg", "reader", "")
	if res.Code != 502 || strings.Contains(res.Body.String(), "secret") {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
}

func TestAccRemoteSourceNetworkWaitHoldsNoStoreTransaction(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(ctx context.Context, _ source.Request, _ source.Permit) (source.Result, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return source.Result{}, ctx.Err()
				}
				return source.Result{
					Stream: &source.Stream{
						Body:           io.NopCloser(strings.NewReader("ok")),
						Status:         200,
						Representation: source.Representation{Size: 2},
					},
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- w.do(http.MethodGet, "/artifactory/up/a.pkg", "reader", "") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("source not entered")
	}
	written := make(chan error, 1)
	go func() { written <- w.db.Settings().Put(t.Context(), "source-concurrency-proof", true) }()
	select {
	case err := <-written:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("source wait blocks unrelated store write")
	}
	close(release)
	if res := <-done; res.Code != 200 {
		t.Fatal(res.Code, res.Body)
	}
}

type failingSourceBody struct{}

func (failingSourceBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingSourceBody) Close() error             { return nil }

func TestAccRemoteSourceBrokenStreamAborts(t *testing.T) {
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{
					Stream: &source.Stream{
						Body:           failingSourceBody{},
						Status:         200,
						Representation: source.Representation{Size: -1},
					},
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("broken stream did not abort: %v", got)
		}
	}()
	w.do(http.MethodGet, "/artifactory/up/a.pkg", "reader", "")
}

func TestAccRemoteRoutePreservesSignedQuery(t *testing.T) {
	var query string
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
				query = q.Resource.Query
				return source.Result{
					Stream: &source.Stream{
						Body:           io.NopCloser(strings.NewReader("data")),
						Status:         200,
						Representation: source.Representation{Size: 4},
					},
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	got := w.do(
		"GET",
		"/artifactory/up/a.pkg?z=%20&skipUpdateStats=true&z=+&encoded=%2f&x=1&x=2",
		"reader",
		"",
	)
	if got.Code != 200 || query != "z=%20&z=+&encoded=%2f&x=1&x=2" {
		t.Fatalf("query %q status%d", query, got.Code)
	}
}

func TestAccRemoteDeliveryRefusesRevocationAfterRead(t *testing.T) {
	body := &sourceBody{Reader: strings.NewReader("must not disclose")}
	checks := 0
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{
					Stream: &source.Stream{
						Body:           body,
						Status:         200,
						Representation: source.Representation{Size: 17},
					},
					Revalidate: func(context.Context) error {
						checks++
						if checks > 1 {
							return source.ErrDenied
						}
						return nil
					},
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	response := w.do("GET", "/artifactory/up/file.pkg", "reader", "")
	if response.Code != 403 || strings.Contains(response.Body.String(), "must not disclose") ||
		body.closes.Load() != 1 ||
		checks != 2 {
		t.Fatal(
			"delivery crossed revoked authority",
			response.Code,
			response.Body,
			body.closes.Load(),
			checks,
		)
	}
}
