package remote_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

func listingFixture(t *testing.T) (*policyWorld, *source.Service, source.Request) {
	t.Helper()
	w := policyFixture(t)
	w.p.Options = func(context.Context, repo.Config) (remote.Options, error) {
		return remote.Options{
			Revision: "credential-1",
			CheckURL: func(_ context.Context, u *url.URL) error {
				if !strings.HasPrefix(u.String(), w.up.URL+"/") {
					return remote.ErrSecurity
				}
				return nil
			},
		}, nil
	}
	subject := accessapi.Subject{Name: "reader", Admin: true}
	access, err := accesstest.Over(t, w.db).Decide(t.Context(), subject, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &source.Service{
		Resolve: func(ctx context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
			return w.db.RemoteCache().Source(ctx, c.Key)
		},
		HTTP: w.p, Cache: w.p,
	}
	return w, s, source.Request{Repo: w.c, Subject: subject, Access: access}
}

func indexHTML(body string) fakeupstream.Resource {
	return fakeupstream.Resource{
		Body:   []byte("<!DOCTYPE html><title>Index of files</title>" + body),
		Header: http.Header{"Content-Type": {"text/html"}},
	}
}

func TestAccListingHTMLUsesFinalBaseFiltersPathsAndCachesOnlyMetadata(t *testing.T) {
	w, s, q := listingFixture(t)
	w.up.Set("/nested/", fakeupstream.Resource{Redirect: "/actual/"})
	w.up.Set(
		"/actual/",
		indexHTML(`<a href="a%20b.txt?secret=hidden">file</a><a href="next/">child</a>
<a href="../parent">parent</a><a href="%2e%2e/out">encoded</a><a href="/elsewhere/private">outside</a>
<a href="https://foreign.invalid/a">foreign</a><a href="%5cprivate">backslash</a><a href="a%20b.txt">duplicate</a>`),
	)
	got, err := remote.ListFolder(t.Context(), s, q, "nested", &remote.ListingBudget{})
	if err != nil {
		t.Fatal(err)
	}
	want := []remote.ListingEntry{
		{Path: "nested/a b.txt", Folder: false},
		{Path: "nested/next", Folder: true},
	}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("entries %+v", got.Entries)
	}
	if err = got.Revalidate(t.Context()); err != nil {
		t.Fatal(err)
	}
	children, _, err := w.db.Items().Children(t.Context(), w.c.Key, true, "", kernel.Page{})
	if err != nil || len(children) != 0 {
		t.Fatalf("manufactured items %v %v", children, err)
	}
	if w.up.Count("GET", "/actual/a%20b.txt") != 0 {
		t.Fatal("downloaded listed child")
	}
	before := len(w.up.Requests())
	q.CacheOnly = true
	w.clock.Advance(time.Hour)
	w.up.Delete("/actual/")
	again, err := remote.ListFolder(t.Context(), s, q, "nested", &remote.ListingBudget{})
	if err != nil || !reflect.DeepEqual(again.Entries, want) || len(w.up.Requests()) != before {
		t.Fatalf("cache-only %+v %v calls=%d", again.Entries, err, len(w.up.Requests()))
	}
	if _, err = remote.ListFolder(
		t.Context(),
		s,
		q,
		"absent",
		&remote.ListingBudget{},
	); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatal("cache miss", err)
	}
	if len(w.up.Requests()) != before {
		t.Fatal("cache miss initiated network")
	}
}

func TestAccListingPermissionFilteringAndFinalRevocation(t *testing.T) {
	w, s, q := listingFixture(t)
	w.up.Set(
		"/",
		indexHTML(
			`<a href="public/">directory</a><a href="secret/">secret</a><a href="hidden.txt">hidden</a>`,
		),
	)
	if err := w.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "narrow", Resources: map[accessapi.ResourceType]accesstest.Resource{
			accessapi.ResourceArtifact: {
				Actions: accesstest.Grants{
					Users: map[string]accessapi.Actions{"limited": accessapi.ActionRead},
				},
				Targets: map[string]accesstest.Target{
					w.c.Key: {Include: []string{"public/allowed.txt"}},
				},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	q.Subject = accessapi.Subject{Name: "limited"}
	limited, err := accesstest.Over(t, w.db).Decide(t.Context(), q.Subject, false)
	if err != nil {
		t.Fatal(err)
	}
	q.Access = limited
	revoked := false
	s.Revalidate = func(_ context.Context, current source.Request, _ source.Permit) error {
		if revoked && current.Resource.PermissionPath == "public" {
			return source.ErrDenied
		}
		return nil
	}
	got, err := remote.ListFolder(t.Context(), s, q, "", &remote.ListingBudget{})
	if err != nil ||
		!reflect.DeepEqual(got.Entries, []remote.ListingEntry{{Path: "public", Folder: true}}) {
		t.Fatalf("narrow folder %+v %v", got.Entries, err)
	}
	revoked = true
	if err = got.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatal("revoked folder disclosed", err)
	}
	q.Repo.ExcludesPattern = ptr("public/**")
	got, err = remote.ListFolder(t.Context(), s, q, "", &remote.ListingBudget{})
	if err != nil || len(got.Entries) != 0 {
		t.Fatalf("pattern filtering %+v %v", got.Entries, err)
	}
}

func TestAccListingNoStoreFailuresAndSourceFence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response fakeupstream.Resource
		expected error
	}{
		{"empty-index", indexHTML(""), nil},
		{"no-index", fakeupstream.Resource{Body: []byte("<html>Login required</html>"), Header: http.Header{"Content-Type": {"text/html"}}}, source.ErrUnsupported},
		{"denied", fakeupstream.Resource{Status: 401}, &source.HTTPError{Status: 401}},
		{"server-error", fakeupstream.Resource{Status: 500}, &source.HTTPError{Status: 502}},
		{"wrong-mime", fakeupstream.Resource{Body: []byte("{}"), Header: http.Header{"Content-Type": {"application/json"}}}, &source.HTTPError{Status: 502}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, s, q := listingFixture(t)
			q.Repo.StoreArtifactsLocally = ptr(false)
			w.up.Set("/", tc.response)
			got, err := remote.ListFolder(t.Context(), s, q, "", &remote.ListingBudget{})
			if tc.expected == nil {
				if err != nil || len(got.Entries) != 0 {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failure hidden")
			}
			entries, _, err := w.db.RemoteCache().List(t.Context(), w.c.Key, "", kernel.Page{})
			if err != nil || len(entries) != 0 {
				t.Fatalf("no-store persisted %v %v", entries, err)
			}
		})
	}
	w, s, q := listingFixture(t)
	w.up.Set("/", indexHTML(`<a href="file.txt">file</a>`))
	got, err := remote.ListFolder(t.Context(), s, q, "", &remote.ListingBudget{})
	if err != nil {
		t.Fatal(err)
	}
	w.c.URL += "/new"
	if err = w.db.Repos().Update(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	if err = got.Revalidate(t.Context()); !errors.Is(err, source.ErrSuperseded) {
		t.Fatal("changed source disclosed", err)
	}
}

type listingBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (b listingBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return b(ctx, q, p)
}

func TestAccListingPaginationIsBoundedAcrossFolders(t *testing.T) {
	_, s, q := listingFixture(t)
	calls := 0
	s.HTTP = listingBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			calls++
			body := fmt.Sprintf(
				`<h1>Index of</h1><a href="a.txt">a</a><link rel="next" href="?page=%d">`,
				calls+1,
			)
			return source.Result{
				Metadata: source.MetadataResponse{BaseURL: "https://up.example/"},
				Stream: &source.Stream{
					Status:         200,
					Body:           io.NopCloser(strings.NewReader(body)),
					Representation: source.Representation{Size: int64(len(body))},
				},
			}, nil
		},
	)
	if got, err := remote.ListFolder(
		t.Context(),
		s,
		q,
		"",
		&remote.ListingBudget{},
	); err == nil || calls != 100 ||
		len(got.Entries) != 0 {
		t.Fatalf("unbounded or partial pages %d %+v %v", calls, got, err)
	}
	calls = 0
	if _, err := remote.ListFolder(
		t.Context(),
		s,
		q,
		"",
		new(remote.ListingBudget).SetPages(100),
	); err == nil ||
		calls != 0 {
		t.Fatal("shared budget bypassed", err)
	}
	for _, folder := range []string{"../x", "/x", "x/", "a//b"} {
		if _, err := remote.ListFolder(
			t.Context(),
			s,
			q,
			folder,
			&remote.ListingBudget{},
		); !errors.Is(
			err,
			source.ErrInvalidRequest,
		) {
			t.Fatal(folder, err)
		}
	}
}

func TestAccListingPaginationCompletesOrFailsWithoutPartialResults(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(fmt.Sprintf("cycle=%t", cycle), func(t *testing.T) {
			_, s, q := listingFixture(t)
			calls := 0
			s.HTTP = listingBackend(
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					calls++
					body := `<h1>Index of</h1><a href="first.txt">first</a><link rel="next" href="?page=2">`
					if strings.HasSuffix(q.Resource.ExternalURL, "?page=2") {
						body = `<h1>Index of</h1><a href="second.txt">second</a><a href="first.txt">duplicate</a>`
						if cycle {
							body += `<link rel="next" href="?page=2">`
						}
					}
					return source.Result{
						Metadata: source.MetadataResponse{BaseURL: "https://up.example/"},
						Stream: &source.Stream{
							Status:         200,
							Body:           io.NopCloser(strings.NewReader(body)),
							Representation: source.Representation{Size: int64(len(body))},
						},
					}, nil
				},
			)
			got, err := remote.ListFolder(t.Context(), s, q, "", &remote.ListingBudget{})
			if calls != 2 || (err != nil) != cycle {
				t.Fatalf("calls=%d result=%+v error=%v", calls, got, err)
			}
			if cycle && len(got.Entries) != 0 {
				t.Fatal("partial listing disclosed", got)
			}
			if !cycle &&
				!reflect.DeepEqual(
					got.Entries,
					[]remote.ListingEntry{
						{Path: "first.txt", Folder: false},
						{Path: "second.txt", Folder: false},
					},
				) {
				t.Fatal(got.Entries)
			}
		})
	}
	w, s, q := listingFixture(t)
	w.up.Set("/", indexHTML(`<a href="a">a</a><a href="b">b</a>`))
	got, err := remote.ListFolder(t.Context(), s, q, "", new(remote.ListingBudget).SetEntries(9999))
	if err == nil || len(got.Entries) != 0 {
		t.Fatal("aggregate entry budget bypassed", got, err)
	}
}
