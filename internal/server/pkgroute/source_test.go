package pkgroute_test

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

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f remoteBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

type remoteBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *remoteBody) Close() error { b.closes.Add(1); return nil }

func TestAccRemoteAdapterExplicitGateAndRead(t *testing.T) {
	calls := 0
	var result source.Result
	var fail error
	svc := &source.Service{
		Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
			return types.SourceIdentity{
				Kind:       types.ExternalHTTP,
				Repository: "repo",
				ID:         "source",
				Generation: "1",
			}, nil
		},
		HTTP: remoteBackend(
			func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
				calls++
				if q.Resource.EscapedPath != "a%20b" {
					t.Errorf("escaped path %s", q.Resource.EscapedPath)
				}
				return result, fail
			},
		),
	}
	f := newKitFixture(
		t,
		map[repo.Class]pkgroute.Source{
			repo.Local:  pkgroute.Local{},
			repo.Remote: pkgroute.Remote{Service: svc},
		},
	)
	if err := f.db.Permissions().
		Create(t.Context(), types.Permission{Name: "read-remote", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}, Targets: map[string]types.Target{"gen-remote": {Include: []string{"**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	b := &remoteBody{Reader: strings.NewReader("remote bytes")}
	result = source.Result{
		Stream: &source.Stream{
			Body:           b,
			Status:         200,
			Representation: source.Representation{Size: 12},
		},
	}
	res := f.do(http.MethodGet, base+"gen-remote/files/a%20b", "reader", "")
	if res.Code != 200 || res.Body.String() != "remote bytes" || b.closes.Load() != 1 {
		t.Fatalf("%d %s closed%d", res.Code, res.Body, b.closes.Load())
	}
	result = source.Result{Item: &types.Item{RepoKey: "gen-remote", Name: "a b", Cached: true}}
	res = f.do(http.MethodGet, base+"gen-remote/files/a%20b", "reader", "")
	if res.Code != 200 || res.Body.String() != "served a b" {
		t.Fatal(res.Code, res.Body)
	}
	result = source.Result{}
	fail = &source.HTTPError{Status: 401}
	res = f.do(http.MethodGet, base+"gen-remote/files/a%20b", "reader", "")
	if res.Code != 401 || res.Header().Get("WWW-Authenticate") != "" ||
		!strings.Contains(res.Body.String(), `"error"`) {
		t.Fatalf("protocol error %d %s %+v", res.Code, res.Body, res.Header())
	}
	before := calls
	res = f.do(http.MethodGet, base+"gen-remote/files/a%20b", "writer", "")
	if res.Code != 403 || calls != before {
		t.Fatal("unauthorized fetch")
	}
	if _, err := (pkgroute.Remote{Service: svc}).Item(
		t.Context(),
		f.db,
		repo.Config{},
		"a",
	); !errors.Is(
		err,
		source.ErrUnsupported,
	) {
		t.Fatal(err)
	}
	if _, err := (pkgroute.Remote{}).Read(
		httptest.NewRequest(http.MethodGet, "/", nil),
		repo.Config{},
		source.Resource{},
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Fatal(err)
	}
	// No default protocol registration changes when the server's common service exists.
	g := newKitFixture(t, nil)
	if res := g.do(http.MethodGet, base+"gen-remote/files/a", "reader", ""); res.Code != 501 {
		t.Fatal(res.Code, res.Body)
	}
}

func TestAccReadExtensionLocalFallback(t *testing.T) {
	f := newKitFixture(t, nil)
	p := testFormat(nil)
	p.Routes = func(r chi.Router, k *pkgroute.Kit) {
		r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			resource := pkgroute.Resource(r, pkgroute.RequestFrom(r).Rest, "")
			if r.URL.Query().Get("metadata") == "true" {
				resource.Kind = source.Metadata
			}
			result, err := k.Read(r, resource)
			if err != nil {
				k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
			_, _ = io.WriteString(w, result.Item.FullPath())
		})
	}
	f.h = f.router(t, f.o, p)
	for _, tc := range []struct {
		path, user string
		status     int
	}{{"a/b.txt", "reader", 200}, {"absent", "reader", 404}, {"a/b.txt?metadata=true", "reader", 501}, {"a/b.txt", "writer", 403}} {
		if res := f.do(http.MethodGet, base+local+"/"+tc.path, tc.user, ""); res.Code != tc.status {
			t.Fatalf("%s %d %s", tc.path, res.Code, res.Body)
		}
	}
}

func TestAccSourceErrorEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		err       error
		anonymous bool
		status    int
	}{
		{source.ErrDenied, false, 403},
		{source.ErrDenied, true, 401},
		{source.ErrInvalidRequest, false, 400},
		{source.ErrUnsupported, false, 501},
		{source.ErrUnavailable, false, 502},
		{source.ErrInvalidResult, false, 502},
		{source.ErrSuperseded, false, 503},
		{context.Canceled, false, 502},
		{context.DeadlineExceeded, false, 504},
		{store.ErrNotFound, false, 404},
		{errors.New("fault"), false, 500},
		{format.Rejectf(404, "excluded"), false, 404},
		{&source.HTTPError{Status: 429, RetryAfter: 1500 * time.Millisecond}, false, 429},
	} {
		res := httptest.NewRecorder()
		apierror.Write(res, apierror.NPM, pkgroute.SourceError(tc.err, tc.anonymous))
		if res.Code != tc.status {
			t.Fatalf("%v: %d", tc.err, res.Code)
		}
		if tc.status == 429 && res.Header().Get("Retry-After") != "2" {
			t.Fatal(res.Header())
		}
	}
	if pkgroute.SourceError(nil, false) != nil {
		t.Fatal("nil became error")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestAccServeTransientRepresentation(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		status       int
		size         int64
		reader       io.Reader
		fail         bool
	}{
		{"get", http.MethodGet, 200, 4, strings.NewReader("data"), false},
		{"unknown", http.MethodGet, 200, -1, strings.NewReader("data"), false},
		{"head", http.MethodHead, 200, 4, brokenReader{}, false},
		{"conditional", http.MethodGet, 304, 4, brokenReader{}, false},
		{"range", http.MethodGet, 206, 4, strings.NewReader("data"), false},
		{"truncated", http.MethodGet, 200, 8, strings.NewReader("data"), true},
		{"broken", http.MethodGet, 200, -1, brokenReader{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &remoteBody{Reader: tc.reader}
			s := &source.Stream{
				Body:   b,
				Status: tc.status,
				Representation: source.Representation{
					Size:         tc.size,
					MediaType:    "application/test",
					ETag:         `"exact"`,
					Modified:     time.Unix(1000, 0),
					Checksums:    types.Checksums{SHA256: "sha256"},
					ContentRange: "bytes 0-3/8",
				},
			}
			r := httptest.NewRequest(tc.method, "/", nil)
			w := httptest.NewRecorder()
			err := pkgroute.ServeStream(w, r, s)
			if (err != nil) != tc.fail || b.closes.Load() != 1 || w.Code != tc.status {
				t.Fatalf("status%d error%v closed%d", w.Code, err, b.closes.Load())
			}
			if w.Header().Get("ETag") != `"exact"` || w.Header().Get("Last-Modified") == "" ||
				w.Header().Get("X-Checksum-Sha256") != "sha256" {
				t.Fatal(w.Header())
			}
			if (tc.method == http.MethodHead || tc.status == 304) && w.Body.Len() != 0 {
				t.Fatal("unexpected body")
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/prefix/a%2Fb;c=d/other%20file?token=one", nil)
	r.Header.Set("Authorization", "secret")
	r.Header.Set("If-None-Match", `"downstream"`)
	escaped := pkgroute.EscapedResourcePath(r, "/prefix/")
	got := pkgroute.Resource(r, "a/b/other file", escaped)
	if got.EscapedPath != "a%2Fb/other%20file" || got.IfNoneMatch != `"downstream"` ||
		got.Query != "token=one" {
		t.Fatalf("%+v", got)
	}
}

func TestAccRemoteNativeWritesRemainRefused(t *testing.T) {
	f := newKitFixture(t, map[repo.Class]pkgroute.Source{repo.Remote: pkgroute.Remote{}})
	p := testFormat(map[repo.Class]pkgroute.Source{repo.Remote: pkgroute.Remote{}})
	p.Routes = func(r chi.Router, k *pkgroute.Kit) {
		r.Put("/{operation}", func(w http.ResponseWriter, r *http.Request) {
			var err error
			switch chi.URLParam(r, "operation") {
			case "store":
				_, err = k.Store(r, "a", r.Body, nil, types.Checksums{})
			case "once":
				_, err = k.StoreOnce(r, "a", r.Body, nil, types.Checksums{}, nil)
			case "new":
				_, err = k.StoreNew(r, "a", blob.Info{}, nil, types.Checksums{}, nil)
			case "upload":
				_, err = k.Upload(r, r.Body)
			}
			k.Error(w, r, err)
		})
	}
	f.h = f.router(t, f.o, p)
	for _, operation := range []string{"store", "once", "new", "upload"} {
		if res := f.do(
			http.MethodPut,
			base+"gen-remote/"+operation,
			"reader",
			"content",
		); res.Code != 405 {
			t.Fatalf("%s: %d %s", operation, res.Code, res.Body)
		}
	}
}
