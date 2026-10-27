package source_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitDeliveryIdentityFailureCannotReuseArtifactAuthority(t *testing.T) {
	t.Parallel()
	for _, cacheOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("cacheOnly=%t", cacheOnly), func(t *testing.T) {
			q := request(t, "")
			q.CacheOnly = cacheOnly
			var resolveErr error
			reads := 0
			read := backend(
				func(context.Context, source.Request, source.Permit) (source.Result, error) {
					reads++
					return itemResult(), nil
				},
			)
			svc := source.Service{
				Resolve: func(ctx context.Context, cfg repo.Config) (types.SourceIdentity, error) {
					if resolveErr != nil {
						return types.SourceIdentity{}, resolveErr
					}
					return identity(ctx, cfg)
				},
				HTTP: read, Cache: read,
			}
			result, err := svc.Read(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			if err := result.CheckRead(t.Context(), q.Resource.Path); err != nil {
				t.Fatal(err)
			}
			if err := result.CheckFolder(
				t.Context(),
				q.Resource.Path,
			); !errors.Is(
				err,
				source.ErrDenied,
			) {
				t.Fatalf("artifact authority widened to folder: %v", err)
			}
			resolveErr = errors.New("lookup https://user:secret@private.invalid/repository failed")
			if err := result.Revalidate(t.Context()); err != source.ErrUnavailable {
				t.Fatalf("late resolution failure was not safely fenced: %v", err)
			}
			if err := result.CheckRead(t.Context(), q.Resource.Path); err != source.ErrUnavailable {
				t.Fatalf("disclosure reused stale identity: %v", err)
			}
			if reads != 1 {
				t.Fatalf("delivery checks invoked backend %d times, want 1", reads)
			}
		})
	}
}

func TestUnitAdapterCredentialsAndGitRequestsFailBeforeResolution(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*source.Request)
	}{
		{"CocoaPods Git on another format", func(q *source.Request) {
			q.Resource.CocoaPodsGit = &source.CocoaPodsGitSource{URL: "https://example.invalid/repo.git"}
		}},
		{"CocoaPods Git without validation contract", func(q *source.Request) {
			q.Repo.PackageType = repo.CocoaPods
			q.Resource.CocoaPodsGit = &source.CocoaPodsGitSource{URL: "https://example.invalid/repo.git"}
		}},
		{"generic transient POST", func(q *source.Request) {
			q.Resource.Method, q.Resource.Transient = http.MethodPost, true
		}},
		{"action credentials on ordinary artifact", func(q *source.Request) {
			q.Resource.GitLFSActionHeaders = http.Header{"Authorization": {"Bearer action-secret"}}
		}},
		{"Git LFS object without pinned checksum", func(q *source.Request) {
			q.Resource.Variant = "gitlfs-object-v1"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := request(t, "")
			tc.mutate(&q)
			assertRequestRejectedBeforeResolution(t, q)
		})
	}
}

func assertRequestRejectedBeforeResolution(t *testing.T, q source.Request) {
	t.Helper()
	svc := source.Service{
		Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
			t.Fatal("invalid adapter contract reached source resolution")
			return types.SourceIdentity{}, nil
		},
	}
	result, err := svc.Read(t.Context(), q)
	if !errors.Is(err, source.ErrInvalidRequest) || result.Item != nil || result.Stream != nil {
		t.Fatalf("invalid adapter admitted: result=%+v error=%v", result, err)
	}
}

func TestUnitGitLFSLockQueriesKeepClosedTransientContract(t *testing.T) {
	t.Parallel()
	q := request(t, "")
	q.Repo.PackageType = repo.GitLFS
	q.Resource = source.Resource{
		Kind:                source.ResourceKindMetadata,
		Path:                "locks",
		UpstreamPath:        "locks",
		Method:              http.MethodGet,
		CheckURL:            func(context.Context, *url.URL) error { return nil },
		Variant:             "gitlfs-locks-v1",
		Transient:           true,
		PermissionFolder:    true,
		Accept:              "application/vnd.git-lfs+json",
		ExpectedContentType: "application/vnd.git-lfs+json",
		ValidateMetadata:    func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil },
		Query:               "path=dir%2Ffile&limit=10&refspec=main",
	}
	svc := source.Service{
		Resolve: identity,
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return stream(io.NopCloser(strings.NewReader(`{"locks":[]}`))), nil
		}),
	}
	result, err := svc.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Stream.Body.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*source.Resource)
	}{
		{"persistent lock listing", func(r *source.Resource) { r.Transient = false }},
		{"conditional stale listing", func(r *source.Resource) { r.IfNoneMatch = `"old-locks"` }},
		{"malformed query escape", func(r *source.Resource) { r.Query = "path=%xy" }},
		{"unsupported query", func(r *source.Resource) { r.Query = "operation=unlock" }},
		{"duplicate query", func(r *source.Resource) { r.Query = "path=one&path=two" }},
		{"oversized query value", func(r *source.Resource) { r.Query = "path=" + strings.Repeat("a", 2049) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := q
			tc.mutate(&bad.Resource)
			assertRequestRejectedBeforeResolution(t, bad)
		})
	}
}

func TestUnitGitLFSBatchCannotExpandDownloadAuthority(t *testing.T) {
	t.Parallel()
	const oid = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	q := request(t, "")
	q.Repo.PackageType = repo.GitLFS
	q.Resource = source.Resource{
		Kind:                source.ResourceKindMetadata,
		Path:                "objects/01/23/" + oid,
		Method:              http.MethodPost,
		UpstreamPath:        "objects/batch",
		Variant:             "gitlfs-batch-v1",
		Transient:           true,
		Accept:              "application/vnd.git-lfs+json",
		ExpectedContentType: "application/vnd.git-lfs+json",
		ValidateMetadata:    func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil },
		PostBody: fmt.Appendf(
			nil,
			`{"operation":"download","objects":[{"oid":%q,"size":1}]}`,
			oid,
		),
	}
	svc := source.Service{
		Resolve: identity,
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return stream(io.NopCloser(strings.NewReader(`{"objects":[]}`))), nil
		}),
	}
	result, err := svc.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Stream.Body.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*source.Resource)
	}{
		{"nontransient batch", func(r *source.Resource) { r.Transient = false }},
		{"upload operation", func(r *source.Resource) {
			r.PostBody = []byte(strings.ReplaceAll(string(r.PostBody), "download", "upload"))
		}},
		{"different object identity", func(r *source.Resource) {
			r.PostBody = []byte(strings.ReplaceAll(string(r.PostBody), oid, strings.Repeat("a", 64)))
		}},
		{"short object identity", func(r *source.Resource) {
			r.PostBody = []byte(strings.ReplaceAll(string(r.PostBody), oid, "abc"))
		}},
		{"noncanonical object identity", func(r *source.Resource) {
			r.PostBody = []byte(strings.ReplaceAll(string(r.PostBody), oid, strings.ToUpper(oid)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := q
			tc.mutate(&bad.Resource)
			assertRequestRejectedBeforeResolution(t, bad)
		})
	}
}
