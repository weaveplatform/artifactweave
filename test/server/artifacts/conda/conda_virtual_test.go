package conda_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	condafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// condaVirtualWorld is a channel served through conda-virtual, which lists
// the nested virtual repository inner, itself, conda-local and inner again,
// where inner lists conda-remote, conda-local and itself: the walk reaches
// conda-remote and conda-local once each (decision 0008, R2). The remote's
// upstream lists the versions of the fixture package that upstream holds,
// or answers 404 for the whole channel when it holds none.
type condaVirtualWorld struct {
	*itemWorld
	q   *work.Queue
	now time.Time
	mu  sync.Mutex
	up  []string
}

func newCondaVirtualWorld(t *testing.T) *condaVirtualWorld {
	t.Helper()
	cw := &condaVirtualWorld{now: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), up: []string{"2.0.0"}}
	body := condaFixture(t)
	fixture, _ := condafmt.ParsePackagePath("noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2")
	rec, err := condafmt.ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)), fixture)
	if err != nil {
		t.Fatal(err)
	}
	rec.SHA256, rec.Size = fmt.Sprintf("%x", sha256.Sum256(body)), int64(len(body))
	sources := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			cw.mu.Lock()
			versions := slices.Clone(cw.up)
			cw.mu.Unlock()
			if len(versions) == 0 {
				return source.Result{}, kernel.ErrNotFound
			}
			index := condafmt.EmptyRepodata("noarch")
			for _, v := range versions {
				r := rec
				r.Version = v
				index.Packages["artifactweave-conda-fixture-"+v+"-0.tar.bz2"] = r
			}
			encoded, err := condafmt.EncodeRepodata(index, "", "noarch")
			if err != nil {
				return source.Result{}, err
			}
			payload := encoded["repodata.json"]
			return source.Result{Stream: &source.Stream{
				Body:           io.NopCloser(bytes.NewReader(payload)),
				Status:         http.StatusOK,
				Representation: source.Representation{Size: int64(len(payload))},
			}}, nil
		},
	)}
	cw.itemWorld = newItemWorld(t, false, func(cfg *Config) {
		var err error
		cw.q, err = work.New(cfg.Store, work.Options{})
		if err != nil {
			t.Fatal(err)
		}
		svc := &item.Service{Store: cfg.Store, Blobs: cfg.Blobs, Work: cw.q, Formats: ecosystem.For}
		if err := svc.RegisterWork(cw.q); err != nil {
			t.Fatal(err)
		}
		cfg.Work = cw.q
		cfg.RemoteSources = sources
		cfg.VirtualCache = virtual.NewCache(virtual.CacheOptions{Now: func() time.Time {
			cw.mu.Lock()
			defer cw.mu.Unlock()
			return cw.now
		}})
	})
	for _, c := range []repo.Config{
		{Key: "conda-local", Class: repo.Local, PackageType: repo.Conda},
		{Key: "conda-remote", Class: repo.Remote, PackageType: repo.Conda, URL: "https://up.example"},
		{Key: "inner", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"conda-remote", "conda-local", "inner"}},
		{Key: "conda-virtual", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"inner", "conda-virtual", "conda-local", "inner"}},
	} {
		if err := cw.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return cw
}

// upstream sets the versions the remote's upstream lists.
func (cw *condaVirtualWorld) upstream(versions ...string) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.up = versions
}

// advance moves the virtual metadata cache's clock on by d.
func (cw *condaVirtualWorld) advance(d time.Duration) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.now = cw.now.Add(d)
}

// deploy stores the fixture package in conda-local and indexes it.
func (cw *condaVirtualWorld) deploy() {
	cw.t.Helper()
	wantStatus(cw.t, cw.do(http.MethodPut,
		"/artifactory/conda-local/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2",
		"admin", string(condaFixture(cw.t))), http.StatusCreated)
	if err := cw.q.EnsureFresh(cw.t.Context(), condafmt.KindIndex, "conda-local"); err != nil {
		cw.t.Fatal(err)
	}
}

// versions returns the fixture versions conda-virtual's repodata.json lists
// to user, in order.
func (cw *condaVirtualWorld) versions(user string) []string {
	cw.t.Helper()
	got := cw.do(http.MethodGet, "/artifactory/api/conda/conda-virtual/noarch/repodata.json", user, "")
	wantStatus(cw.t, got, http.StatusOK)
	return listedVersions(cw.t, got)
}

func listedVersions(t *testing.T, got *httptest.ResponseRecorder) []string {
	t.Helper()
	id, _ := condafmt.ParseMetadataPath("noarch/repodata.json")
	index, err := condafmt.DecodeRepodata(got.Body.Bytes(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, rec := range index.Packages {
		out = append(out, rec.Version)
	}
	slices.Sort(out)
	return out
}

// A virtual repository's index reflects its local members at once and keeps
// each remote member's part for virtualRetrievalCachePeriodSecs: JFrog's
// Conda page says its metadata reflects "changes in the aggregated local
// repositories in real-time", while "for aggregated remote repositories,
// the virtual repository metadata is renewed on demand" by the "Retrieval
// Cache Period parameter of your virtual repository" (decision 0008, R8). A
// remote member whose upstream then answers 404 drops its kept part and is
// passed over (R3). The walk reaches each member once through the nested
// virtual repository, the cycle and the duplicate (R2), where main answered
// 400.
func TestAccCondaVirtualLocalMembersAreLiveAndRemotePartsKeptForThePeriod(t *testing.T) {
	cw := newCondaVirtualWorld(t)
	if got := cw.versions("admin"); !slices.Equal(got, []string{"2.0.0"}) {
		t.Fatalf("remote member through nested members: %v", got)
	}
	cw.deploy()
	if got := cw.versions("admin"); !slices.Equal(got, []string{"1.0.0", "2.0.0"}) {
		t.Fatalf("a new local package did not show at once: %v", got)
	}
	cw.upstream("3.0.0")
	if got := cw.versions("admin"); !slices.Equal(got, []string{"1.0.0", "2.0.0"}) {
		t.Fatalf("the remote part was not kept for the period: %v", got)
	}
	archive := cw.do(http.MethodGet,
		"/artifactory/api/conda/conda-virtual/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2", "admin", "")
	wantStatus(t, archive, http.StatusOK)
	cw.advance(time.Duration(repo.DefaultVirtualRetrievalCachePeriod+1) * time.Second)
	if got := cw.versions("admin"); !slices.Equal(got, []string{"1.0.0", "3.0.0"}) {
		t.Fatalf("the remote part was not renewed after the period: %v", got)
	}
	cw.upstream()
	cw.advance(time.Duration(repo.DefaultVirtualRetrievalCachePeriod+1) * time.Second)
	if got := cw.versions("admin"); !slices.Equal(got, []string{"1.0.0"}) {
		t.Fatalf("a remote member whose upstream answers 404 was not passed over: %v", got)
	}
}

// A caller who may read only the local member, with no "ANY" target, reads
// the virtual repository's index and packages, with the remote member it
// may not read passed over; one who may read no member is refused the
// index, as the root rule refuses it (pkgroute.Kit.RequireRoot), and gets
// Conda's not-found for a package (decision 0008, R3).
func TestAccCondaVirtualServesAMemberOnlyReader(t *testing.T) {
	cw := newCondaVirtualWorld(t)
	cw.deploy()
	if err := cw.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "conda-local-only",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accesstest.Target{"conda-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := cw.versions("user"); !slices.Equal(got, []string{"1.0.0"}) {
		t.Fatalf("a member-only reader's index: %v", got)
	}
	pkg := "/artifactory/api/conda/conda-virtual/noarch/artifactweave-conda-fixture-1.0.0-0.tar.bz2"
	wantStatus(t, cw.do(http.MethodGet, pkg, "user", ""), http.StatusOK)
	wantStatus(t, cw.do(http.MethodGet,
		"/artifactory/api/conda/conda-virtual/noarch/repodata.json", "deployer", ""), http.StatusForbidden)
	wantStatus(t, cw.do(http.MethodGet, pkg, "deployer", ""), http.StatusNotFound)
}
