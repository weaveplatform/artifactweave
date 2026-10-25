package bower

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type bowerSourceRead func(context.Context, source.Request, source.Permit) (source.Result, error)

func (fn bowerSourceRead) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return fn(ctx, q, p)
}

type brokenMetadataBody struct{}

func (brokenMetadataBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenMetadataBody) Close() error             { return nil }

func metadataStream(body io.ReadCloser) source.Result {
	return source.Result{Stream: &source.Stream{
		Body: body, Status: http.StatusOK,
		Representation: source.Representation{Size: -1},
	}}
}

func TestUnitBowerAdapterFailsClosedOnInterruptedMetadataStreams(t *testing.T) {
	for _, git := range []bool{false, true} {
		for _, faulty := range []string{"catalog", "pointer", "refs"} {
			if !git && faulty == "pointer" {
				continue
			}
			t.Run(
				strings.Join(
					[]string{map[bool]string{true: "git", false: "artifactory"}[git], faulty},
					"/",
				),
				func(t *testing.T) {
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
					backend := bowerSourceRead(
						func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
							if strings.Contains(q.Resource.Path, "pointer") && faulty != "pointer" {
								return metadataStream(
									io.NopCloser(
										strings.NewReader(
											`{"name":"hello","url":"https://upstream.example/git/team/hello.git"}`,
										),
									),
								), nil
							}
							return metadataStream(brokenMetadataBody{}), nil
						},
					)
					f.mount(&source.Service{
						HTTP:  backend,
						Cache: backend,
						Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
							return f.db.RemoteCache().Source(ctx, c.Key)
						},
					})
					path := "/artifactory/api/bower/bower-remote/packages"
					if faulty != "catalog" {
						path = "/artifactory/api/bower/bower-remote/refs/bower-remote/hello"
					}
					if r := f.get(path, "admin"); r.Code == http.StatusOK {
						t.Fatalf("interrupted %s metadata was served", faulty)
					}
				},
			)
		}
	}
}

func TestUnitBowerSourceBackendErrorDoesNotExposeMetadata(t *testing.T) {
	f := newFixture(t)
	c := f.repo("bower-remote", repo.Remote)
	c.URL = "https://upstream.example/api/bower"
	c.VCSGitProvider = repo.ArtifactoryVCS
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	backend := bowerSourceRead(
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return source.Result{}, errors.New("upstream unavailable")
		},
	)
	f.mount(&source.Service{
		HTTP: backend, Cache: backend,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	})
	for _, path := range []string{"packages", "refs/bower-remote/hello"} {
		if r := f.get(
			"/artifactory/api/bower/bower-remote/"+path,
			"admin",
		); r.Code == http.StatusOK {
			t.Fatalf("upstream backend error leaked %s", path)
		}
	}
}

func TestUnitBowerVirtualDoesNotHideMemberSourceFailure(t *testing.T) {
	f := newFixture(t)
	c := f.repo("bower-remote", repo.Remote)
	c.URL = "https://upstream.example/api/bower"
	c.VCSGitProvider = repo.ArtifactoryVCS
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	_ = f.repo("bower-virtual", repo.Virtual, "bower-remote")
	backend := bowerSourceRead(
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return source.Result{}, source.ErrUnavailable
		},
	)
	f.mount(&source.Service{
		HTTP: backend, Cache: backend,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	})
	for _, path := range []string{"packages", "refs/bower-virtual/hello"} {
		if r := f.get(
			"/artifactory/api/bower/bower-virtual/"+path,
			"admin",
		); r.Code == http.StatusOK {
			t.Fatalf("virtual repository hid a failed member source for %s", path)
		}
	}
}

func TestUnitBowerGitMetadataAdmissionChecksDeclaredSizes(t *testing.T) {
	for _, tc := range []struct {
		name, target, path string
		size               int64
	}{
		{"catalog", "catalog/packages.json", "packages", -1},
		{"catalog truncated", "catalog/packages.json", "packages", 1},
		{"pointer", "catalog/hello/pointer.json", "refs/bower-remote/hello", 0},
		{"pointer truncated", "catalog/hello/pointer.json", "refs/bower-remote/hello", 1},
		{"smart refs", "catalog/hello/git-refs", "refs/bower-remote/hello", 0},
		{"smart refs truncated", "catalog/hello/git-refs", "refs/bower-remote/hello", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			c := f.repo("bower-remote", repo.Remote)
			c.URL = "https://upstream.example/git"
			c.BowerRegistryURL = "https://registry.example"
			c.VCSGitProvider = repo.GitHubEnterprise
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			validated := false
			backend := bowerSourceRead(
				func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					if q.Resource.Path == "catalog/hello/pointer.json" &&
						tc.target != q.Resource.Path {
						return metadataStream(
							io.NopCloser(
								strings.NewReader(
									`{"name":"hello","url":"https://upstream.example/git/team/hello.git"}`,
								),
							),
						), nil
					}
					if q.Resource.Path != tc.target {
						t.Fatalf("unexpected metadata request: %s", q.Resource.Path)
					}
					validated = true
					if q.Resource.ValidateMetadata == nil {
						t.Fatal("metadata was missing its admission validator")
					}
					var data []byte
					switch tc.target {
					case "catalog/packages.json":
						data = []byte(`[]`)
					case "catalog/hello/pointer.json":
						data = []byte(
							`{"name":"hello","url":"https://upstream.example/git/team/hello.git"}`,
						)
					default:
						data = []byte(gitPkt(strings.Repeat("a", 40) + " refs/tags/v1.0.0\n"))
					}
					if err := q.Resource.ValidateMetadata(
						ctx,
						bytes.NewReader(data),
						tc.size,
						source.MetadataResponse{},
					); err == nil {
						t.Fatal("metadata validator admitted an invalid declared size")
					}
					return source.Result{}, source.ErrInvalidResult
				},
			)
			f.mount(&source.Service{
				HTTP: backend, Cache: backend,
				Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
					return f.db.RemoteCache().Source(ctx, c.Key)
				},
			})
			if r := f.get(
				"/artifactory/api/bower/bower-remote/"+tc.path,
				"admin",
			); r.Code == http.StatusOK ||
				!validated {
				t.Fatalf("size admission was bypassed: status=%d validated=%t", r.Code, validated)
			}
		})
	}
}
