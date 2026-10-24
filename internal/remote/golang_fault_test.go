package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccGoPrivateCredentialsAndRepositoryIsolation(t *testing.T) {
	w, src, q := goSourceFixture(t)
	up := fakeupstream.New(
		t,
		fakeupstream.Options{
			Auth: fakeupstream.Auth{Username: "upstream", Password: "go-fixture-secret"},
		},
	)
	w.c.URL, w.c.Username, w.c.Password = up.URL, "upstream", "go-fixture-secret"
	if err := w.db.Repos().Update(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(
		w.db,
		nil,
		ResolverOptions{RevisionKey: []byte("go-test-only-credential-revision-key")},
	)
	if err != nil {
		t.Fatal(err)
	}
	w.p.Options = resolver.Options
	q.Repo = repo.Masked(w.c)
	path := goEscapedModule + "/@v/v1.0.0.mod"
	body := []byte("module " + goModule + "\n")
	up.Set("/"+path, fakeupstream.Resource{Body: body})
	readGo(t, w, src, q, path, body)
	for _, request := range up.Requests() {
		if !request.Authorized {
			t.Fatal("configured upstream credentials were not used")
		}
	}
	other := w.c
	other.Key, other.Password = "other", "wrong-credential"
	if err = w.db.Repos().Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	q.Repo, q.CacheOnly, q.Resource = repo.Masked(other), true, source.Resource{Path: path}
	before := up.Count("", "")
	if _, err = src.Read(t.Context(), q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("cross-repository cache hit", err)
	}
	if up.Count("", "") != before {
		t.Fatal("cache-only miss fetched")
	}
	q.CacheOnly = false
	if _, err = src.Read(
		t.Context(),
		q,
	); err == nil ||
		strings.Contains(err.Error(), other.Password) {
		t.Fatal("private upstream denial or redaction failed")
	}
	if up.Count("", "") <= before {
		t.Fatal("did not exercise other repository credentials")
	}
	// A future virtual resolver must preserve these source-level denials; it
	// cannot treat an authentication failure as permission to contact direct VCS.
	if _, err = w.db.Items().
		Get(t.Context(), other.Key, true, goModule+"/@v/v1.0.0.mod"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("denied private artifact published", err)
	}
}

func TestAccGoSourceDenialAndInterruptedBodies(t *testing.T) {
	w, src, q := goSourceFixture(t)
	path := goEscapedModule + "/@v/v1.0.0.mod"
	body := []byte("module " + goModule + "\n")
	w.up.Set("/"+path, fakeupstream.Resource{Body: body})
	q.Resource = source.Resource{Path: path}
	q.Access = nil
	if _, err := src.Read(t.Context(), q); !errors.Is(err, source.ErrDenied) {
		t.Fatal(err)
	}
	if w.up.Count("", "") != 0 {
		t.Fatal("denied path contacted upstream")
	}
	w, src, q = goSourceFixture(t)
	w.up.Set(
		"/"+path,
		fakeupstream.Resource{Body: body, Fault: fakeupstream.Fault{TruncateAfter: 4}},
	)
	q.Resource = source.Resource{Path: path}
	if _, err := src.Read(t.Context(), q); err == nil {
		t.Fatal("partial body accepted")
	}
	if _, err := w.db.Items().
		Get(t.Context(), q.Repo.Key, true, goModule+"/@v/v1.0.0.mod"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("partial body published", err)
	}
	w.up.Set("/"+path, fakeupstream.Resource{Body: body})
	readGo(t, w, src, q, path, body)
}

func TestAccGoSourcePreservesConsumerIntentAndFences(t *testing.T) {
	w, src, q := goSourceFixture(t)
	underlying := src.Source.(*source.Service)
	denied := false
	underlying.Revalidate = func(_ context.Context, got source.Request, _ source.Permit) error {
		if got.Project != "consumer" || len(got.Visited) != 1 {
			t.Fatal("consumer context dropped")
		}
		if denied {
			return source.ErrDenied
		}
		return nil
	}
	q.Project, q.Visited = "consumer", []types.RepositoryIncarnation{"previous-hop"}
	path := goEscapedModule + "/@v/v1.0.0.mod"
	body := []byte("module " + goModule + "\n")
	w.up.Set("/"+path, fakeupstream.Resource{Body: body})
	out := readGo(t, w, src, q, path, body)
	denied = true
	if err := out.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatal("delivery fence failed", err)
	}
	q.Repo.Class = repo.Local
	if _, err := src.Read(t.Context(), q); !errors.Is(err, source.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestAccGoSourceInputCannotSelectAnExternalTarget(t *testing.T) {
	w, src, q := goSourceFixture(t)
	path := goEscapedModule + "/@v/v1.0.0.mod"
	body := []byte("module " + goModule + "\n")
	w.up.Set("/"+path, fakeupstream.Resource{Body: body})
	q.Resource = source.Resource{
		Path:             path,
		ExternalURL:      "http://169.254.169.254/",
		PermissionFolder: true,
		PermissionPath:   "",
		ChecksumOnly:     true,
	}
	out, err := src.Read(t.Context(), q)
	if err != nil || w.bytes(out) != string(body) {
		t.Fatal(err)
	}
	if w.up.Count(http.MethodGet, "/"+path) != 1 {
		t.Fatal("trusted canonical target was not used")
	}
}

func TestAccGoValidationFaults(t *testing.T) {
	info, _ := GoResource(goModule+"/@v/v1.0.0.info", "GET")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := info.Validate(ctx, strings.NewReader(""), 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := info.Validate(
		t.Context(),
		strings.NewReader("short"),
		12,
	); !errors.Is(
		err,
		ErrGoResponse,
	) {
		t.Fatal(err)
	}
	latest, _ := GoResource(goModule+"/@latest", "GET")
	for _, body := range []string{"null", "{}", "[]", `{"Version":1}`, `{"Version":"v1.0.0","Time":"bad"}`} {
		if err := latest.Validate(
			t.Context(),
			strings.NewReader(body),
			int64(len(body)),
		); !errors.Is(
			err,
			ErrGoResponse,
		) {
			t.Fatal(body, err)
		}
	}
	archive, _ := GoResource(goModule+"/@v/v1.0.0.zip", "GET")
	for _, size := range []int64{-1, 501 << 20} {
		if err := archive.Validate(
			t.Context(),
			bytes.NewReader(nil),
			size,
		); !errors.Is(
			err,
			ErrGoResponse,
		) {
			t.Fatal(size, err)
		}
	}
	if err := archive.Validate(
		t.Context(),
		bytes.NewReader([]byte("bad")),
		4,
	); !errors.Is(
		err,
		ErrGoResponse,
	) {
		t.Fatal(err)
	}
	if err := archive.Validate(
		t.Context(),
		bytes.NewReader([]byte("bad")),
		3,
	); !errors.Is(
		err,
		ErrGoResponse,
	) {
		t.Fatal(err)
	}
	badReader := &goBrokenSeeker{}
	if err := archive.Validate(t.Context(), badReader, 3); err == nil {
		t.Fatal("read failure ignored")
	}
}

type goBrokenSeeker struct{}

func (*goBrokenSeeker) Read([]byte) (int, error)       { return 0, io.ErrUnexpectedEOF }
func (*goBrokenSeeker) Seek(int64, int) (int64, error) { return 0, io.ErrUnexpectedEOF }

func TestAccGoSourceMissingVersusUpstreamFailure(t *testing.T) {
	for _, status := range []int{404, 410, 401, 403, 429, 500, 503} {
		for _, persist := range []bool{true, false} {
			t.Run(strconv.Itoa(status)+"/persist="+strconv.FormatBool(persist), func(t *testing.T) {
				w, src, q := goSourceFixture(t)
				q.Repo.StoreArtifactsLocally = ptr(persist)
				path := goEscapedModule + "/@v/v1.0.0.mod"
				q.Resource = source.Resource{Path: path}
				w.up.Set("/"+path, fakeupstream.Resource{Status: status})
				_, err := src.Read(t.Context(), q)
				missing := status == 404 || status == 410
				if err == nil || errors.Is(err, store.ErrNotFound) != missing {
					t.Fatalf("status %d must preserve absence versus failure: %v", status, err)
				}
				if !missing {
					var failure *source.HTTPError
					if !errors.As(err, &failure) {
						t.Fatal("upstream failure lost classification", err)
					}
					return
				}
				body := []byte("module " + goModule + "\n")
				w.up.Set("/"+path, fakeupstream.Resource{Body: body})
				if persist {
					before := w.up.Count("", "")
					if _, err = src.Read(t.Context(), q); !errors.Is(err, store.ErrNotFound) {
						t.Fatal("negative cache bypass", err)
					}
					if w.up.Count("", "") != before {
						t.Fatal("negative cache contacted upstream")
					}
					w.clock.Advance(31 * time.Second)
				}
				readGo(t, w, src, q, path, body)
			})
		}
	}
}

func TestAccGoGoneOptInDoesNotChangeGenericErrors(t *testing.T) {
	w := policyFixture(t)
	w.up.Set("/gone.bin", fakeupstream.Resource{Status: http.StatusGone})
	q := w.request("gone.bin", source.Artifact)
	_, err := w.p.Read(t.Context(), q, w.permit)
	if err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatal("generic 410 became a protocol absence", err)
	}
}
