package source_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type backend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f backend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

func identity(context.Context, repo.Config) (types.SourceIdentity, error) {
	return types.SourceIdentity{
		Kind:       types.ExternalHTTP,
		Repository: "created-1",
		ID:         "source-1",
		Generation: "revision-1",
	}, nil
}

func request(t *testing.T, scope string) source.Request {
	t.Helper()
	st := inmem.New()
	ctx := t.Context()
	if err := st.Permissions().
		Create(ctx, types.Permission{Name: "read", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
				Targets: map[string]types.Target{"up": {Include: []string{"**"}}},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	subject := authz.Subject{Name: "reader"}
	if scope != "" {
		sc, err := types.ParseScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		subject.Scope = &sc
	}
	access, err := authz.New(
		ctx,
		authz.Sources{Permissions: st.Permissions(), Groups: st.Groups()},
		subject,
	)
	if err != nil {
		t.Fatal(err)
	}
	return source.Request{
		Repo:    repo.Config{Key: "up", Class: repo.Remote},
		Subject: subject,
		Access:  access,
		Resource: source.Resource{
			Kind:        source.Artifact,
			Path:        "a.pkg",
			EscapedPath: "a.pkg",
			Method:      http.MethodGet,
		},
	}
}

type body struct {
	io.Reader
	closes atomic.Int32
	closed chan struct{}
}

func (b *body) Close() error {
	if b.closes.Add(1) == 1 && b.closed != nil {
		close(b.closed)
	}
	return nil
}

func stream(b io.ReadCloser) source.Result {
	return source.Result{
		Stream: &source.Stream{
			Body:           b,
			Status:         200,
			Representation: source.Representation{Size: -1},
		},
	}
}

func itemResult() source.Result {
	return source.Result{Item: &types.Item{RepoKey: "up", Name: "a.pkg", Cached: true}}
}

func TestReadIntentsAndRepresentations(t *testing.T) {
	for _, kind := range []source.ResourceKind{source.Artifact, source.Metadata, source.Versions} {
		for _, cache := range []bool{false, true} {
			t.Run(fmt.Sprintf("kind%d/cache%t", kind, cache), func(t *testing.T) {
				q := request(t, "")
				q.Resource.Kind, q.CacheOnly = kind, cache
				q.Resource.EscapedPath, q.Resource.Query, q.Resource.Variant = "a%2epkg", "version=1", "json"
				calls, validates := 0, 0
				read := backend(
					func(_ context.Context, got source.Request, p source.Permit) (source.Result, error) {
						calls++
						if !reflect.DeepEqual(got.Resource, q.Resource) ||
							got.Subject.Name != "reader" ||
							p.Identity.Generation != "revision-1" {
							t.Fatalf("request lost: %+v %+v", got, p)
						}
						if p.UpstreamNetworkAllowed == cache ||
							(p.Intent == source.CacheOnly) != cache {
							t.Fatalf("wrong permit %+v", p)
						}
						return itemResult(), nil
					},
				)
				s := &source.Service{
					Resolve:    identity,
					Revalidate: func(context.Context, source.Request, source.Permit) error { validates++; return nil },
				}
				if cache {
					s.Cache = read
				} else {
					s.HTTP = read
				}
				got, err := s.Read(t.Context(), q)
				if err != nil || got.Item == nil || got.Identity.Generation != "revision-1" ||
					calls != 1 ||
					validates != 2 {
					t.Fatalf(
						"result %+v error %v calls %d validations %d",
						got,
						err,
						calls,
						validates,
					)
				}
			})
		}
	}
}

func TestRefusalsPrecedeSourceLookup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*source.Request)
	}{
		{"unresolved permission", func(q *source.Request) { q.Access = nil }},
		{"wrong class", func(q *source.Request) { q.Repo.Class = repo.Virtual }},
		{"unknown kind", func(q *source.Request) { q.Resource.Kind = 0 }},
		{"write method", func(q *source.Request) { q.Resource.Method = http.MethodPut }},
		{"root", func(q *source.Request) { q.Resource.Path = "" }},
		{"absolute", func(q *source.Request) { q.Resource.Path = "/a.pkg" }},
		{"traversal", func(q *source.Request) { q.Resource.Path = "../a.pkg" }},
		{"empty segment", func(q *source.Request) { q.Resource.Path = "a//b"; q.Resource.EscapedPath = "" }},
		{"control", func(q *source.Request) { q.Resource.Path = "a\nb" }},
		{"escaped mismatch", func(q *source.Request) { q.Resource.EscapedPath = "../secret" }},
		{"malformed escaped path", func(q *source.Request) { q.Resource.EscapedPath = "%xx" }},
		{"escaped query", func(q *source.Request) { q.Resource.Path = "a?b"; q.Resource.EscapedPath = "a?b" }},
		{"backslash", func(q *source.Request) { q.Resource.Path = "a\\b" }},
		{"blackout", func(q *source.Request) { q.Repo.BlackedOut = new(true) }},
		{"excluded", func(q *source.Request) { q.Repo.ExcludesPattern = new("**") }},
		{"not included", func(q *source.Request) { q.Repo.IncludesPattern = new("other/**") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := request(t, "")
			tc.change(&q)
			s := source.Service{
				Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
					t.Fatal("unauthorized source lookup")
					return types.SourceIdentity{}, nil
				},
			}
			if _, err := s.Read(t.Context(), q); err == nil {
				t.Fatal("read allowed")
			}
		})
	}
	q := request(t, "applied-permissions/user artifact:other:r")
	if _, err := (&source.Service{}).Read(t.Context(), q); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("scope bypass: %v", err)
	}
}

func TestUnsupportedAndUnresolvedSources(t *testing.T) {
	q := request(t, "")
	for _, tc := range []struct {
		name    string
		service source.Service
		cached  bool
		want    error
	}{
		{"no resolver", source.Service{}, false, source.ErrUnsupported},
		{"no backend", source.Service{Resolve: identity}, false, source.ErrUnsupported},
		{"cache miss never fetches", source.Service{Resolve: identity, HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			t.Fatal("network from cache only")
			return source.Result{}, nil
		})}, true, store.ErrNotFound},
		{"missing repo", source.Service{Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
			return types.SourceIdentity{}, store.ErrNotFound
		}}, false, store.ErrNotFound},
		{"project source", source.Service{Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			id, _ := identity(ctx, c)
			id.Kind = types.ProjectRepository
			return id, nil
		}}, false, source.ErrUnsupported},
		{"incomplete identity", source.Service{Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
			return types.SourceIdentity{Kind: types.ExternalHTTP}, nil
		}}, false, source.ErrUnavailable},
		{"authorization revoked", source.Service{Resolve: identity, Revalidate: func(context.Context, source.Request, source.Permit) error { return source.ErrDenied }}, false, source.ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q.CacheOnly = tc.cached
			if _, err := tc.service.Read(t.Context(), q); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (&source.Service{}).Read(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestResultValidationAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result source.Result
		err    error
		want   error
	}{
		{"neither", source.Result{}, nil, source.ErrInvalidResult},
		{"both", source.Result{Item: itemResult().Item, Stream: stream(io.NopCloser(strings.NewReader("x"))).Stream}, nil, source.ErrInvalidResult},
		{"other repository", source.Result{Item: &types.Item{RepoKey: "secret", Name: "a.pkg", Cached: true}}, nil, source.ErrInvalidResult},
		{"wrong path", source.Result{Item: &types.Item{RepoKey: "up", Name: "b.pkg", Cached: true}}, nil, source.ErrInvalidResult},
		{"not cached", source.Result{Item: &types.Item{RepoKey: "up", Name: "a.pkg"}}, nil, source.ErrInvalidResult},
		{"folder", source.Result{Item: &types.Item{RepoKey: "up", Name: "a.pkg", Cached: true, Folder: true}}, nil, source.ErrInvalidResult},
		{"nil body", source.Result{Stream: &source.Stream{Status: 200}}, nil, source.ErrInvalidResult},
		{"bad size", source.Result{Stream: &source.Stream{Status: 200, Body: io.NopCloser(strings.NewReader("x")), Representation: source.Representation{Size: -2}}}, nil, source.ErrInvalidResult},
		{"bad status", source.Result{Stream: &source.Stream{Status: 404, Body: io.NopCloser(strings.NewReader("x"))}}, nil, source.ErrInvalidResult},
		{"confirmed missing", source.Result{}, fmt.Errorf("secret URL: %w", store.ErrNotFound), store.ErrNotFound},
		{"unknown fault", source.Result{}, errors.New("https://user:secret@host/path?token=secret"), source.ErrUnavailable},
		{"unexpected upstream status", source.Result{}, &source.HTTPError{Status: 201}, source.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := source.Service{
				Resolve: identity,
				HTTP: backend(
					func(context.Context, source.Request, source.Permit) (source.Result, error) { return tc.result, tc.err },
				),
			}
			_, err := s.Read(t.Context(), request(t, ""))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("credential leak")
			}
		})
	}
	for _, status := range []int{401, 403, 429, 500, 502, 504, 599} {
		for _, retry := range []time.Duration{-1, 10 * time.Second, 10 * time.Minute} {
			s := source.Service{
				Resolve: identity,
				HTTP: backend(
					func(context.Context, source.Request, source.Permit) (source.Result, error) {
						return source.Result{}, fmt.Errorf(
							"secret: %w",
							&source.HTTPError{
								Status:     status,
								RetryAfter: retry,
								Kind:       source.HTTPFailure,
							},
						)
					},
				),
			}
			_, err := s.Read(t.Context(), request(t, ""))
			var got *source.HTTPError
			if !errors.As(err, &got) || got.Status != status ||
				strings.Contains(err.Error(), "secret") {
				t.Fatalf("bad error %v", err)
			}
			if retry < 0 {
				retry = 0
			}
			if retry > 5*time.Minute {
				retry = 5 * time.Minute
			}
			if got.RetryAfter != retry {
				t.Fatal(got)
			}
		}
	}
}

func TestStreamOwnershipAndLateFences(t *testing.T) {
	for _, mode := range []string{"returned", "error", "revoked", "generation", "resolver failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b := &body{Reader: strings.NewReader("bytes"), closed: make(chan struct{})}
			resolves, validates := 0, 0
			s := source.Service{
				Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
					resolves++
					id, _ := identity(ctx, c)
					if resolves > 1 {
						if mode == "generation" {
							id.Generation = "revision-2"
						}
						if mode == "resolver failure" {
							return id, store.ErrNotFound
						}
					}
					return id, nil
				},
				Revalidate: func(context.Context, source.Request, source.Permit) error {
					validates++
					if validates > 1 && mode == "revoked" {
						return source.ErrDenied
					}
					return nil
				},
				HTTP: backend(
					func(context.Context, source.Request, source.Permit) (source.Result, error) {
						if mode == "error" {
							return stream(b), errors.New("secret")
						}
						if mode == "canceled" {
							cancel()
						}
						return stream(b), nil
					},
				),
			}
			result, err := s.Read(ctx, request(t, ""))
			if mode == "returned" {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				select {
				case <-b.closed:
				case <-time.After(time.Second):
					t.Fatal("cancellation did not close stream")
				}
				result.Stream.Body.Close()
				result.Stream.Body.Close()
			} else if err == nil {
				t.Fatal("late failure accepted")
			}
			if b.closes.Load() != 1 {
				t.Fatalf("close count %d", b.closes.Load())
			}
		})
	}
	b := &body{Reader: strings.NewReader("bytes")}
	s := source.Service{
		Resolve: identity,
		HTTP: backend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) { return stream(b), nil },
		),
	}
	result, err := s.Read(t.Context(), request(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := io.ReadAll(result.Stream.Body)
	if err != nil || string(bytes) != "bytes" {
		t.Fatalf("%q %v", bytes, err)
	}
	result.Stream.Body.Close()
	if b.closes.Load() != 1 {
		t.Fatal("unclosed")
	}
}

type seekBody struct {
	*strings.Reader
	closed bool
}

func (b *seekBody) Close() error { b.closed = true; return nil }
func TestValidatedStreamRemainsSeekable(t *testing.T) {
	b := &seekBody{Reader: strings.NewReader("0123456789")}
	s := source.Service{
		Resolve: identity,
		HTTP: backend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) { return stream(b), nil },
		),
	}
	result, err := s.Read(t.Context(), request(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	seeker, ok := result.Stream.Body.(io.ReadSeeker)
	if !ok {
		t.Fatal("lost seek capability")
	}
	if pos, err := seeker.Seek(4, io.SeekStart); err != nil || pos != 4 {
		t.Fatalf("seek %d %v", pos, err)
	}
	content, err := io.ReadAll(result.Stream.Body)
	if err != nil || string(content) != "456789" {
		t.Fatalf("%q %v", content, err)
	}
	if _, err := seeker.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("invalid seek accepted")
	}
	result.Stream.Body.Close()
	if !b.closed {
		t.Fatal("lost body ownership")
	}
}

func TestBackendRevalidationAndChecksumAuthority(t *testing.T) {
	q := request(t, "artifact:up/a.pkg:r")
	q.Resource.Path = "a.pkg.sha256"
	q.Resource.EscapedPath = q.Resource.Path
	q.Resource.ChecksumOnly = true
	b := &body{Reader: strings.NewReader("sum")}
	service := source.Service{
		Resolve: identity,
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return source.Result{
				Stream: &source.Stream{
					Body:           b,
					Status:         200,
					Representation: source.Representation{Size: 3},
				},
				Revalidate: func(context.Context) error { return source.ErrSuperseded },
			}, nil
		}),
	}
	if _, err := service.Read(
		t.Context(),
		q,
	); !errors.Is(err, source.ErrSuperseded) ||
		b.closes.Load() != 1 {
		t.Fatalf("fence %v closed%d", err, b.closes.Load())
	}
	for _, path := range []string{"a.pkg.sha1", ".sha256"} {
		q.Resource.Path = path
		q.Resource.EscapedPath = path
		if _, err := service.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatalf("invalid checksum %q %v", path, err)
		}
	}
}

func TestAdapterTargetNeverReplacesAuthorizationPath(t *testing.T) {
	q := request(t, "")
	pattern := "a.pkg"
	q.Repo.IncludesPattern = &pattern
	q.Resource.UpstreamPath = "private/origin.pkg"
	calls := 0
	s := &source.Service{
		Resolve: identity,
		HTTP: backend(
			func(_ context.Context, got source.Request, _ source.Permit) (source.Result, error) {
				calls++
				if got.Resource.Path != "a.pkg" ||
					got.Resource.UpstreamPath != "private/origin.pkg" {
					t.Fatal(got.Resource)
				}
				return itemResult(), nil
			},
		),
	}
	if _, err := s.Read(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q.Resource.Path, q.Resource.EscapedPath = "denied.pkg", "denied.pkg"
	if _, err := s.Read(t.Context(), q); err == nil || calls != 1 {
		t.Fatal("target bypassed canonical policy", err, calls)
	}
	q.Resource.Path, q.Resource.EscapedPath = "a.pkg", "a.pkg"
	for _, invalid := range []string{"%zz", "/absolute", "a?secret", "../escape", "a%0ab"} {
		q.Resource.UpstreamPath = invalid
		if _, err := s.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatal(invalid, err)
		}
	}
	q.Resource.UpstreamPath, q.Resource.ExternalURL = "valid", "https://up.example/file"
	if _, err := s.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatal(err)
	}
	q.Resource.UpstreamPath, q.Resource.ExternalURL = "", ""
	q.Resource.Validate = func(context.Context, io.ReadSeeker, int64) error { return nil }
	if _, err := s.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatal("validation needs schema variant", err)
	}
}

func TestMetadataFolderPermissionAndDeliveryFence(t *testing.T) {
	q := request(t, "")
	include := "pkg/-/**"
	q.Repo.IncludesPattern = &include
	q.Resource.Kind, q.Resource.Path, q.Resource.EscapedPath = source.Metadata, "packument", "packument"
	q.Resource.PermissionFolder, q.Resource.PermissionPath = true, "pkg"
	var revoked bool
	svc := source.Service{
		Resolve: identity,
		Revalidate: func(context.Context, source.Request, source.Permit) error {
			if revoked {
				return source.ErrDenied
			}
			return nil
		},
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return stream(io.NopCloser(strings.NewReader("{}"))), nil
		}),
	}
	result, err := svc.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Stream.Body.Close()
	revoked = true
	if err = result.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("delivery authorization: %v", err)
	}
	revoked = false
	q.Resource.Kind = source.Artifact
	if _, err = svc.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatalf("artifact folder override: %v", err)
	}
	q.Resource.Kind = source.Metadata
	for _, path := range []string{"/pkg", "../pkg"} {
		q.Resource.PermissionPath = path
		if _, err = svc.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatalf("invalid folder %s: %v", path, err)
		}
	}
	q.Resource.PermissionPath = "other"
	if _, err = svc.Read(t.Context(), q); err == nil {
		t.Fatal("repository exclusion accepted")
	}
}

func TestMetadataDisclosuresRevalidateEachPath(t *testing.T) {
	for _, folder := range []string{"pkg", ""} {
		t.Run("folder="+folder, func(t *testing.T) {
			q := request(t, "")
			q.Resource = source.Resource{
				Kind:             source.Metadata,
				Path:             "metadata",
				Method:           "GET",
				PermissionFolder: true,
				PermissionPath:   folder,
			}
			revoked := false
			credentialRevoked := false
			svc := source.Service{
				Resolve: identity,
				Revalidate: func(_ context.Context, current source.Request, _ source.Permit) error {
					if revoked && current.Resource.Path == "pkg/-/pkg-1.0.0.tgz" {
						return source.ErrDenied
					}
					return nil
				},
				HTTP: backend(
					func(context.Context, source.Request, source.Permit) (source.Result, error) {
						out := stream(io.NopCloser(strings.NewReader("{}")))
						out.Revalidate = func(context.Context) error {
							if credentialRevoked {
								return source.ErrSuperseded
							}
							return nil
						}
						return out, nil
					},
				),
			}
			out, err := svc.Read(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Stream.Body.Close()
			first, second := "pkg/-/pkg-1.0.0.tgz", "pkg/-/pkg-2.0.0.tgz"
			if err = out.CheckRead(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			revoked = true
			if err = out.Revalidate(t.Context()); err != nil {
				t.Fatal("folder no longer readable", err)
			}
			if err = out.CheckRead(t.Context(), first); !errors.Is(err, source.ErrDenied) {
				t.Fatal("revoked version disclosed", err)
			}
			if err = out.CheckRead(t.Context(), second); err != nil {
				t.Fatal("retained version denied", err)
			}
			if folder != "" {
				if err = out.CheckRead(
					t.Context(),
					"elsewhere/item",
				); !errors.Is(
					err,
					source.ErrDenied,
				) {
					t.Fatal("scope widened", err)
				}
			}
			if err = out.CheckRead(t.Context(), "pkg/../escape"); err == nil {
				t.Fatal("invalid path accepted")
			}
			credentialRevoked = true
			if err = out.CheckRead(t.Context(), second); !errors.Is(err, source.ErrSuperseded) {
				t.Fatal("credential fence bypassed", err)
			}
		})
	}
}

func TestMetadataMIMEAlternativesRequireExplicitValidatedContract(t *testing.T) {
	for _, change := range []func(*source.Resource){
		func(r *source.Resource) { r.Kind = source.Artifact },
		func(r *source.Resource) { r.Validate = nil },
		func(r *source.Resource) { r.ExpectedContentType = "text/html" },
		func(r *source.Resource) { r.ExpectedContentTypes = []string{"text/html", "text/html"} },
		func(r *source.Resource) { r.ExpectedContentTypes = []string{"text/*"} },
		func(r *source.Resource) { r.ExpectedContentTypes = []string{"Text/HTML"} },
		func(r *source.Resource) { r.ExpectedContentTypes = []string{"text/html; charset=utf-8"} },
		func(r *source.Resource) { r.ExpectedContentTypes = []string{"invalid"} },
	} {
		q := request(t, "")
		q.Resource.Kind = source.Metadata
		q.Resource.Variant = "validated"
		q.Resource.ExpectedContentTypes = []string{"text/html", "application/json"}
		q.Resource.Validate = func(context.Context, io.ReadSeeker, int64) error { return nil }
		change(&q.Resource)
		s := source.Service{
			Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
				t.Fatal("invalid hint reached resolution")
				return types.SourceIdentity{}, nil
			},
		}
		if _, err := s.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatalf("invalid contract %v", err)
		}
	}
}

func TestMetadataResponseValidatorRequiresExplicitContract(t *testing.T) {
	for _, change := range []func(*source.Resource){
		func(r *source.Resource) { r.Kind = source.Artifact },
		func(r *source.Resource) { r.Variant = "" },
		func(r *source.Resource) {
			r.Validate = func(context.Context, io.ReadSeeker, int64) error { return nil }
		},
	} {
		q := request(t, "")
		q.Resource.Kind = source.Metadata
		q.Resource.Variant = "response-base-v1"
		q.Resource.ValidateMetadata = func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil }
		change(&q.Resource)
		s := source.Service{
			Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
				t.Fatal("invalid validator reached resolution")
				return types.SourceIdentity{}, nil
			},
		}
		if _, err := s.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatalf("invalid response contract: %v", err)
		}
	}
}
