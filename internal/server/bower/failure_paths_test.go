package bower

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func scriptedBowerRemote(t *testing.T, git bool, read bowerSourceRead) *fixture {
	t.Helper()
	f := newFixture(t)
	c := f.repo("bower-remote", repo.Remote)
	c.URL = "https://upstream.example/git"
	c.BowerRegistryURL = "https://registry.example"
	if git {
		c.VCSGitProvider = repo.GitHubEnterprise
	} else {
		c.VCSGitProvider = repo.ArtifactoryVCS
	}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.mount(&source.Service{
		HTTP: read, Cache: read,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	})
	return f
}

func bowerMetadata(body string) source.Result {
	return metadataStream(io.NopCloser(strings.NewReader(body)))
}

func TestUnitBowerRemoteBinaryRechecksRefsBeforeFetching(t *testing.T) {
	refs := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	for _, tc := range []struct {
		name string
		next string
		err  error
		want int
	}{
		{"source disappears", "", source.ErrUnavailable, http.StatusBadGateway},
		{"tag disappears", strings.Repeat("b", 44) + " refs/tags/v2.0.0\n", nil, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			f := scriptedBowerRemote(
				t,
				false,
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					if q.Resource.Path != "catalog/hello/refs" {
						t.Fatalf("unexpected upstream read: %s", q.Resource.Path)
					}
					reads++
					if reads == 1 {
						return bowerMetadata(refs), nil
					}
					if tc.err != nil {
						return source.Result{}, tc.err
					}
					return bowerMetadata(tc.next), nil
				},
			)
			r := f.get(
				"/artifactory/api/bower/bower-remote/binaries/bower-remote/hello.git/v1.0.0",
				"admin",
			)
			if r.Code != tc.want || reads != 2 {
				t.Fatalf("binary with %s = %d, reads=%d: %s", tc.name, r.Code, reads, r.Body)
			}
		})
	}
}

func TestUnitBowerGitBinaryRechecksPointerAndTags(t *testing.T) {
	pointer := `{"name":"hello","url":"https://upstream.example/git/team/hello.git"}`
	refs := gitPkt(strings.Repeat("a", 40) + " refs/tags/v1.0.0\n")
	for _, tc := range []struct {
		name       string
		failOn     string
		secondBody string
		err        error
		want       int
	}{
		{"pointer disappears", "catalog/hello/pointer.json", "", source.ErrUnavailable, http.StatusBadGateway},
		{"refs disappear", "catalog/hello/git-refs", "", source.ErrUnavailable, http.StatusBadGateway},
		{"tag disappears", "catalog/hello/git-refs", gitPkt(strings.Repeat("b", 40) + " refs/tags/v2.0.0\n"), nil, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := map[string]int{}
			f := scriptedBowerRemote(
				t,
				true,
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					path := q.Resource.Path
					reads[path]++
					if path == tc.failOn && reads[path] == 2 {
						if tc.err != nil {
							return source.Result{}, tc.err
						}
						return bowerMetadata(tc.secondBody), nil
					}
					switch path {
					case "catalog/hello/pointer.json":
						return bowerMetadata(pointer), nil
					case "catalog/hello/git-refs":
						return bowerMetadata(refs), nil
					default:
						t.Fatalf("unexpected upstream read: %s", path)
						return source.Result{}, nil
					}
				},
			)
			r := f.get(
				"/artifactory/api/bower/bower-remote/binaries/bower-remote/hello.git/v1.0.0",
				"admin",
			)
			if r.Code != tc.want || reads[tc.failOn] != 2 {
				t.Fatalf("Git binary with %s = %d, reads=%v: %s", tc.name, r.Code, reads, r.Body)
			}
		})
	}
}

func TestUnitBowerVirtualArchiveSelectsLaterMember(t *testing.T) {
	f := newFixture(t)
	first := f.repo("bower-first", repo.Local)
	second := f.repo("bower-second", repo.Local)
	_ = f.repo("bower-virtual", repo.Virtual, first.Key, second.Key)
	f.deploy(second, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	f.deploy(second, "hello", "1.0.0", "index.js", "from-second")
	r := f.get(
		"/artifactory/api/bower/bower-virtual/binaries/bower-virtual/hello.git/v1.0.0",
		"admin",
	)
	if r.Code != http.StatusOK || !strings.Contains(r.Header().Get("Content-Type"), "gzip") {
		t.Fatalf("second member archive = %d %s", r.Code, r.Body)
	}
}

func TestUnitBowerVirtualArchiveFailsWhenMemberChangesAfterLookup(t *testing.T) {
	refs := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	for _, tc := range []struct {
		name string
		next string
		err  error
		want int
	}{
		{"source fails", "", source.ErrUnavailable, http.StatusBadGateway},
		{"version disappears", strings.Repeat("b", 44) + " refs/tags/v2.0.0\n", nil, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			f := scriptedBowerRemote(
				t,
				false,
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					if q.Resource.Path != "catalog/hello/refs" {
						t.Fatalf("unexpected upstream read: %s", q.Resource.Path)
					}
					reads++
					if reads == 1 {
						return bowerMetadata(refs), nil
					}
					if tc.err != nil {
						return source.Result{}, tc.err
					}
					return bowerMetadata(tc.next), nil
				},
			)
			_ = f.repo("bower-virtual", repo.Virtual, "bower-remote")
			r := f.get(
				"/artifactory/api/bower/bower-virtual/binaries/bower-virtual/hello.git/v1.0.0",
				"admin",
			)
			if r.Code != tc.want || reads != 2 {
				t.Fatalf(
					"virtual archive with %s = %d, reads=%d: %s",
					tc.name,
					r.Code,
					reads,
					r.Body,
				)
			}
		})
	}
}

func TestUnitBowerRemoteMetadataRequiresAValidatedStream(t *testing.T) {
	pointer := `{"name":"hello","url":"https://upstream.example/git/team/hello.git"}`
	for _, tc := range []struct {
		name, target, route string
		git                 bool
	}{
		{"Artifactory catalog", "catalog/packages.json", "packages", false},
		{"Artifactory refs", "catalog/hello/refs", "refs/bower-remote/hello", false},
		{"Git catalog", "catalog/packages.json", "packages", true},
		{"Git pointer", "catalog/hello/pointer.json", "refs/bower-remote/hello", true},
		{"Git refs", "catalog/hello/git-refs", "refs/bower-remote/hello", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := scriptedBowerRemote(
				t,
				tc.git,
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					if q.Resource.Path == "catalog/hello/pointer.json" &&
						tc.target != q.Resource.Path {
						return bowerMetadata(pointer), nil
					}
					if q.Resource.Path != tc.target {
						t.Fatalf("unexpected metadata request: %s", q.Resource.Path)
					}
					cut := strings.LastIndexByte(tc.target, '/')
					it := types.Item{
						RepoKey: "bower-remote", Cached: true,
						Path: tc.target[:cut], Name: tc.target[cut+1:],
					}
					return source.Result{Item: &it}, nil
				},
			)
			r := f.get("/artifactory/api/bower/bower-remote/"+tc.route, "admin")
			if r.Code == http.StatusOK {
				t.Fatal("metadata adapter served an item without a validated stream")
			}
		})
	}
}

func TestUnitBowerMissingRemoteMetadataDoesNotExposePackages(t *testing.T) {
	for _, git := range []bool{false, true} {
		f := scriptedBowerRemote(
			t,
			git,
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{}, store.ErrNotFound
			},
		)
		for _, route := range []string{"packages", "refs/bower-remote/hello"} {
			r := f.get("/artifactory/api/bower/bower-remote/"+route, "admin")
			if route == "packages" && r.Code != http.StatusNotFound {
				t.Fatalf("missing catalog = %d %s", r.Code, r.Body)
			}
			if route != "packages" && r.Code != http.StatusNotFound {
				t.Fatalf("missing refs = %d %s", r.Code, r.Body)
			}
		}
	}
}

func TestUnitBowerRemoteMetadataRevalidatesBeforeDisclosure(t *testing.T) {
	refs := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	pointer := `{"name":"hello","url":"https://upstream.example/git/team/hello.git"}`
	for _, tc := range []struct {
		name, target, route, body string
		git                       bool
		failAfter                 int
	}{
		{"Artifactory refs", "catalog/hello/refs", "refs/bower-remote/hello", refs, false, 2},
		{"Git pointer", "catalog/hello/pointer.json", "refs/bower-remote/hello", pointer, true, 1},
		{"Git catalog", "catalog/packages.json", "packages", "[" + pointer + "]", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			f := scriptedBowerRemote(
				t,
				tc.git,
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					if q.Resource.Path != tc.target {
						t.Fatalf("unexpected metadata request: %s", q.Resource.Path)
					}
					result := bowerMetadata(tc.body)
					result.Revalidate = func(context.Context) error {
						checks++
						if checks > tc.failAfter {
							return source.ErrUnavailable
						}
						return nil
					}
					return result, nil
				},
			)
			r := f.get("/artifactory/api/bower/bower-remote/"+tc.route, "admin")
			if r.Code == http.StatusOK || checks <= tc.failAfter {
				t.Fatalf("stale metadata disclosure = %d, checks=%d: %s", r.Code, checks, r.Body)
			}
		})
	}
}

func TestUnitBowerArtifactoryCatalogFiltersSearchAndFailsOnBadRefs(t *testing.T) {
	f := scriptedBowerRemote(
		t,
		false,
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			switch q.Resource.Path {
			case "catalog/packages.json":
				return bowerMetadata(
					`[{"name":"hello","url":"art://source/hello"},{"name":"other","url":"art://source/other"}]`,
				), nil
			case "catalog/hello/refs":
				return bowerMetadata("malformed refs"), nil
			default:
				return source.Result{}, store.ErrNotFound
			}
		},
	)
	base := "/artifactory/api/bower/bower-remote/"
	if r := f.get(
		base+"packages/search/absent",
		"admin",
	); r.Code != http.StatusOK ||
		r.Body.String() != "[]\n" {
		t.Fatalf("unmatched search = %d %s", r.Code, r.Body)
	}
	if r := f.get(base+"packages", "admin"); r.Code == http.StatusOK {
		t.Fatal("catalog hid malformed refs for a listed package")
	}
}

func TestUnitBowerMetadataStopsWhenSourceRevisionChanges(t *testing.T) {
	refs := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	pointer := `{"name":"hello","url":"https://upstream.example/git/team/hello.git"}`
	for _, tc := range []struct {
		name, route, body string
		git               bool
	}{
		{"Artifactory refs", "refs/bower-remote/hello", refs, false},
		{"Git catalog", "packages", "[" + pointer + "]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := bowerSourceRead(
				func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return bowerMetadata(tc.body), nil
				},
			)
			f := scriptedBowerRemote(t, tc.git, backend)
			identity, err := f.db.RemoteCache().Source(t.Context(), "bower-remote")
			if err != nil {
				t.Fatal(err)
			}
			resolves := 0
			f.mount(&source.Service{
				HTTP: backend, Cache: backend,
				Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
					resolves++
					current := identity
					if resolves >= 3 {
						current.Generation += "-changed"
					}
					return current, nil
				},
			})
			r := f.get("/artifactory/api/bower/bower-remote/"+tc.route, "admin")
			if r.Code == http.StatusOK || resolves < 3 {
				t.Fatalf("stale %s = %d, resolves=%d: %s", tc.name, r.Code, resolves, r.Body)
			}
		})
	}
}

func TestUnitBowerMetadataStopsWhenAuthorityIsRevoked(t *testing.T) {
	refs := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	pointer := `{"name":"hello","url":"https://upstream.example/git/team/hello.git"}`
	for _, tc := range []struct {
		name, route, body string
		git               bool
	}{
		{"Artifactory refs", "refs/bower-remote/hello", refs, false},
		{"Git catalog", "packages", "[" + pointer + "]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			f := scriptedBowerRemote(
				t,
				tc.git,
				func(context.Context, source.Request, source.Permit) (source.Result, error) {
					result := bowerMetadata(tc.body)
					result.Revalidate = func(context.Context) error {
						checks++
						if checks > 1 {
							return source.ErrDenied
						}
						return nil
					}
					return result, nil
				},
			)
			r := f.get("/artifactory/api/bower/bower-remote/"+tc.route, "admin")
			if r.Code == http.StatusOK || checks < 2 {
				t.Fatalf(
					"revoked %s disclosed metadata: %d, checks=%d: %s",
					tc.name,
					r.Code,
					checks,
					r.Body,
				)
			}
		})
	}
}

func TestUnitBowerGitCatalogRejectsMalformedCachedMetadata(t *testing.T) {
	f := scriptedBowerRemote(
		t,
		true,
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return bowerMetadata(`{"name":"hello"}`), nil
		},
	)
	if r := f.get(
		"/artifactory/api/bower/bower-remote/packages",
		"admin",
	); r.Code == http.StatusOK {
		t.Fatal("malformed cached Git catalog was served")
	}
}

func TestUnitBowerRemoteRefsHideUnreadableManifest(t *testing.T) {
	refs := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	f := scriptedBowerRemote(
		t,
		false,
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return bowerMetadata(refs), nil
		},
	)
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "read-other-bower-file",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
			Targets: map[string]types.Target{
				"bower-remote": {
					Include: []string{"hello/**"}, Exclude: []string{"hello/1.0.0/bower.json"},
				},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	r := f.get("/artifactory/api/bower/bower-remote/refs/bower-remote/hello", "reader")
	if r.Code != http.StatusNotFound {
		t.Fatalf("unreadable manifest disclosed a version: %d %s", r.Code, r.Body)
	}
}

func TestUnitBowerVirtualCatalogSkipsUnreadableMember(t *testing.T) {
	f := newFixture(t)
	private := f.repo("bower-private", repo.Local)
	visible := f.repo("bower-visible", repo.Local)
	_ = f.repo("bower-virtual", repo.Virtual, private.Key, visible.Key)
	f.deploy(private, "secret", "1.0.0", "bower.json", `{"name":"secret","version":"1.0.0"}`)
	f.deploy(visible, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "visible-bower-member",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
			Targets: map[string]types.Target{
				"bower-virtual": {Include: []string{"**"}},
				"bower-visible": {Include: []string{"hello/**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	r := f.get("/artifactory/api/bower/bower-virtual/packages", "reader")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"name":"hello"`) ||
		strings.Contains(r.Body.String(), `"name":"secret"`) {
		t.Fatalf("virtual catalog disclosed an unreadable member: %d %s", r.Code, r.Body)
	}
}

func TestUnitBowerVirtualRechecksMemberConfigurationBeforeArchive(t *testing.T) {
	refs := strings.Repeat("a", 44) + " refs/tags/v1.0.0\n"
	var f *fixture
	changed := false
	backend := bowerSourceRead(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			if q.Resource.Path != "catalog/hello/refs" {
				t.Fatalf("unexpected upstream read: %s", q.Resource.Path)
			}
			if !changed {
				changed = true
				member, err := f.db.Repos().Get(t.Context(), "bower-local")
				if err != nil {
					t.Fatal(err)
				}
				member.Class = repo.Virtual
				if err := f.db.Repos().Update(t.Context(), member); err != nil {
					t.Fatal(err)
				}
			}
			return bowerMetadata(refs), nil
		},
	)
	f = scriptedBowerRemote(t, false, backend)
	_ = f.repo("bower-local", repo.Local)
	_ = f.repo("bower-virtual", repo.Virtual, "bower-remote", "bower-local")
	r := f.get(
		"/artifactory/api/bower/bower-virtual/binaries/bower-virtual/hello.git/v1.0.0",
		"admin",
	)
	if !changed || r.Code == http.StatusOK {
		t.Fatalf("changed virtual member was served: %d %s", r.Code, r.Body)
	}
}
