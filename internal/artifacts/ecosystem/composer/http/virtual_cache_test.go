package composerhttp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// A virtual repository's p2 document is kept for its
// virtualRetrievalCachePeriodSecs (decision 0008, R8): 7.161.26 saves a
// period for a Composer virtual repository (RepoConfigDescriptorBuilder.
// populateVirtualComposerDescriptor) and VirtualRepo.isCachedResourceExpired
// reads it. A member deploy leaves the kept document until it expires, a
// period of 0 builds it on every request, the ~dev document is kept apart,
// and a hosted repository's p2 is never kept.
func TestAccComposerVirtualP2IsKeptForTheRetrievalCachePeriod(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cache := virtual.NewCache(virtual.CacheOptions{Now: func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}})
	f := newFixtureWithCache(t, nil, cache)
	local := f.create("local", repo.Local)
	f.deploy(local, "1.0.0", "first")
	f.deploy(local, "dev-main", "development")
	f.create("all", repo.Virtual, "local")
	fresh := f.create("fresh", repo.Virtual, "local")
	zero := 0
	fresh.VirtualRetrievalCachePeriodSecs = &zero
	if err := f.db.Repos().Update(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	p2 := func(key, doc string) string {
		t.Helper()
		got := f.request("GET", "/artifactory/api/composer/"+key+"/p2/acme/"+doc)
		if got.Code != 200 {
			t.Fatalf("%s %s: %d %s", key, doc, got.Code, got.Body)
		}
		return got.Body.String()
	}
	kept := p2("all", "widget.json")
	keptDev := p2("all", "widget~dev.json")
	if !strings.Contains(kept, `"version":"1.0.0"`) || !strings.Contains(keptDev, `"version":"dev-main"`) {
		t.Fatalf("first merge: %s %s", kept, keptDev)
	}
	_ = p2("fresh", "widget.json")
	f.deploy(local, "1.1.0", "second")
	if got := p2("all", "widget.json"); got != kept {
		t.Fatalf("inside the period a member deploy showed through: %s", got)
	}
	if got := p2("all", "widget~dev.json"); got != keptDev {
		t.Fatalf("inside the period the dev document changed: %s", got)
	}
	for _, key := range []string{"fresh", "local"} {
		if got := p2(key, "widget.json"); !strings.Contains(got, `"version":"1.1.0"`) {
			t.Fatalf("%s did not merge on the request: %s", key, got)
		}
	}
	mu.Lock()
	now = now.Add(7201 * time.Second)
	mu.Unlock()
	if got := p2("all", "widget.json"); !strings.Contains(got, `"version":"1.1.0"`) {
		t.Fatalf("after the period the document was not built again: %s", got)
	}
}

// A virtual repository passes over a remote member whose upstream cannot
// be reached [I] (decision 0008, R3), and logs a warning naming the member
// and the virtual repository (pkgroute.Kit.WarnPassedOver), for its p2
// document and for an archive only that member lists.
func TestAccComposerVirtualWarnsOfAFailingMember(t *testing.T) {
	b := &sourceBackend{
		content:   map[string][]byte{"packages.json": []byte(`{"metadata-url":"/p2/%package%.json"}`)},
		readError: map[string]error{"p2/acme/widget.json": source.ErrUnavailable},
	}
	svc := &source.Service{
		HTTP: b, Cache: b,
		Resolve: func(context.Context, repo.Config) (model.SourceIdentity, error) {
			return model.SourceIdentity{Kind: model.ExternalHTTP, Repository: "remote", ID: "source", Generation: "1"}, nil
		},
	}
	f := newFixtureWithSource(t, svc)
	r := f.create("remote", repo.Remote)
	r.URL, r.ComposerRegistryURL = "https://up.example", "https://up.example"
	if err := f.db.Repos().Update(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	local := f.create("local", repo.Local)
	f.deploy(local, "1.1.0", "local")
	f.create("all", repo.Virtual, "remote", "local")
	warned := func(what string) {
		t.Helper()
		got := f.logs.String()
		f.logs.Reset()
		for _, want := range []string{
			"level=WARN", `msg="virtual repository passed over a failing member"`,
			"member=remote", "virtual=all",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: log %q lacks %q", what, got, want)
			}
		}
	}
	got := f.request("GET", "/artifactory/api/composer/all/p2/acme/widget.json")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"version":"1.1.0"`) {
		t.Fatalf("p2 over a failing member: %d %s", got.Code, got.Body)
	}
	warned("p2")
	got = f.request("GET", "/artifactory/api/composer/all/dist/acme/widget/1.0.0.zip")
	if got.Code != 404 || !strings.Contains(got.Body.String(), "Composer package not found") {
		t.Fatalf("dist only a failing member could list: %d %s", got.Code, got.Body)
	}
	warned("dist")
}

// A virtual repository's p2 document answers its own not-found for a
// document no member lists, and passes over a remote member whose metadata
// fails its recheck (decision 0008, R3). Neither answer is kept, so the
// next request builds the document again, and the first one served is.
func TestAccComposerVirtualP2FailuresAreNotKept(t *testing.T) {
	b := &sourceBackend{content: map[string][]byte{
		"packages.json": []byte(`{"metadata-url":"/p2/%package%.json"}`),
		"p2/acme/widget.json": []byte(
			`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0.0","dist":{"type":"zip","url":"https://up.example/dist.zip"}}]}}`,
		),
	}}
	svc := &source.Service{
		HTTP: b, Cache: b,
		Resolve: func(context.Context, repo.Config) (model.SourceIdentity, error) {
			return model.SourceIdentity{Kind: model.ExternalHTTP, Repository: "remote", ID: "source", Generation: "1"}, nil
		},
	}
	f := newFixtureWithCache(t, svc, virtual.NewCache(virtual.CacheOptions{}))
	r := f.create("remote", repo.Remote)
	r.URL, r.ComposerRegistryURL = "https://up.example", "https://up.example"
	if err := f.db.Repos().Update(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	f.create("all", repo.Virtual, "remote")
	const p2 = "/artifactory/api/composer/all/p2/acme/"
	for _, tc := range []struct {
		doc  string
		err  error
		code int
		body string
	}{
		{"widget~dev.json", nil, 404, "Composer package not found"},
		{"widget.json", source.ErrUnavailable, 404, "Composer package not found"},
		{"widget.json", source.ErrDenied, 404, "Composer package not found"},
		{"widget.json", nil, 200, `"version":"1.0.0"`},
	} {
		b.revalidateErr = tc.err
		got := f.request("GET", p2+tc.doc)
		if got.Code != tc.code || !strings.Contains(got.Body.String(), tc.body) {
			t.Errorf("%s with %v: %d %s", tc.doc, tc.err, got.Code, got.Body)
		}
	}
	// The document built last is kept: a release the upstream adds stays
	// unseen for the period.
	b.content["p2/acme/widget.json"] = []byte(
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"2.0.0","dist":{"type":"zip","url":"https://up.example/dist.zip"}}]}}`,
	)
	if got := f.request("GET", p2+"widget.json"); got.Code != 200 ||
		!strings.Contains(got.Body.String(), `"version":"1.0.0"`) || strings.Contains(got.Body.String(), "2.0.0") {
		t.Fatalf("kept document: %d %s", got.Code, got.Body)
	}
}
