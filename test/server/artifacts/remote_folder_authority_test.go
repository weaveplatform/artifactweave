package artifacts_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type remoteFolderBackend struct {
	source.Backend
	fail func(context.Context) error
}

func TestAccRemoteFolderOnlyOriginAcquisitionFailuresKeepCache(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"upstream-http", &source.HTTPError{Status: 500, Kind: source.FailureKindHTTP}, 200},
		{"upstream-auth", &source.HTTPError{Status: 401, Kind: source.FailureKindHTTP}, 200},
		{"upstream-missing", kernel.ErrNotFound, 200},
		{"unsupported", source.ErrUnsupported, 501},
		{"superseded", source.ErrSuperseded, 503},
		{"denied", source.ErrDenied, 403},
		{"integrity", &source.HTTPError{Status: 502, Kind: source.FailureKindIntegrity}, 502},
		{"security", &source.HTTPError{Status: 403, Kind: source.FailureKindSecurity}, 403},
		{"protocol", &source.HTTPError{Status: 501, Kind: source.FailureKindProtocol}, 501},
		{"own-store", kernel.ErrStore, 500},
		{"own-selected-content", &source.ContentOpenError{Err: kernel.ErrStore}, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := remoteFolderWorld(t, "sqlite")
			remoteFolderCache(t, w, "dir/kept.bin")
			svc := w.Runtime.Artifacts.RemoteSources()
			svc.HTTP = remoteFolderBackend{Backend: svc.HTTP, fail: func(context.Context) error { return tc.err }}
			names := []string(nil)
			if tc.code == 200 {
				names = []string{"kept.bin"}
			}
			wantRemoteFolder(t, w, "GET", "/artifactory/remote/dir/", "admin", tc.code, names)
		})
	}
}

func (b remoteFolderBackend) Read(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
	if q.Resource.Kind == source.ResourceKindMetadata && q.Resource.PermissionFolder && q.Resource.PermissionPath == "dir" {
		return source.Result{}, b.fail(ctx)
	}
	return b.Backend.Read(ctx, q, p)
}

func (b remoteFolderBackend) BindAuthority(ctx context.Context, q source.Request, p source.Permit) (source.AuthorityBinding, error) {
	if bound, ok := b.Backend.(source.AuthorityBackend); ok {
		return bound.BindAuthority(ctx, q, p)
	}
	return source.AuthorityBinding{}, nil
}

func TestAccRemoteFolderCurrentAuthorityAfterOriginFailure(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, change := range []struct {
				name string
				code int
			}{
				{"source", 503}, {"credentials", 503}, {"patterns", 503}, {"permission", 403}, {"canceled-parent", 502},
			} {
				t.Run(change.name, func(t *testing.T) {
					w := remoteFolderWorld(t, backend)
					remoteFolderCache(t, w, "dir/kept.bin")
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					svc := w.Runtime.Artifacts.RemoteSources()
					svc.HTTP = remoteFolderBackend{Backend: svc.HTTP, fail: func(ctx context.Context) error {
						switch change.name {
						case "source":
							w.Repo.URL += "/changed"
							w.Update()
						case "credentials":
							w.Repo.Username = "rotated-user"
							w.Repo.Password = "rotated-test-password"
							w.Update()
						case "patterns":
							w.Repo.ExcludesPattern = ptr("**/*.bin")
							w.Update()
						case "permission":
							if err := w.Runtime.Store.Permissions().Delete(ctx, "remote-read"); err != nil {
								t.Fatal(err)
							}
						case "canceled-parent":
							cancel()
							return ctx.Err()
						}
						return &source.HTTPError{Kind: source.FailureKindHTTP, Status: 503}
					}}
					req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/artifactory/remote/dir/", nil)
					req.SetBasicAuth("reader", "pw")
					rec := httptest.NewRecorder()
					w.H.ServeHTTP(rec, req)
					if rec.Code != change.code || strings.Contains(rec.Body.String(), "kept.bin") || strings.Contains(rec.Body.String(), "Index of") {
						t.Fatalf("changed authority became cached success:%d %s, want%d", rec.Code, rec.Body, change.code)
					}
				})
			}
		})
	}
}

func TestAccRemoteFolderRepeatsCachedChildAuthorityBeforeDisclosure(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, mode := range []string{"cached-first", "live-first"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				w := remoteFolderWorld(t, backend)
				remoteFolderCache(t, w, "dir/z.bin")
				svc := w.Runtime.Artifacts.RemoteSources()
				if mode == "cached-first" {
					remoteFolderCache(t, w, "dir/a.bin")
					svc.HTTP = remoteFolderBackend{Backend: svc.HTTP, fail: func(context.Context) error {
						return &source.HTTPError{Status: 503, Kind: source.FailureKindHTTP}
					}}
				} else {
					w.Upstream.Set("/dir/", upstreamIndex("a.bin"))
				}
				previous := svc.Revalidate
				changed := false
				svc.Revalidate = func(ctx context.Context, q source.Request, p source.Permit) error {
					if err := previous(ctx, q, p); err != nil {
						return err
					}
					// Revoke the earlier file after validating a later file. The
					// parent remains readable through z.bin, so a folder-only fence
					// cannot detect the earlier child's loss of Read.
					if !changed && q.Resource.Path == "dir/z.bin" && !q.Resource.PermissionFolder {
						changed = true
						permission := accesstest.Permission{Name: "remote-read", Resources: map[accessapi.ResourceType]accesstest.Resource{
							accessapi.ResourceArtifact: {
								Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"reader": accessapi.ActionRead | accessapi.ActionWrite}},
								Targets: map[string]accesstest.Target{"remote": {Include: []string{"dir/z.bin"}}},
							},
						}}
						if err := w.Runtime.Store.Permissions().Update(ctx, permission); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				}
				rec := w.Request("GET", "/artifactory/remote/dir/", "reader", "")
				if !changed {
					t.Fatal("selective grant update was not exercised")
				}
				if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "a.bin") || strings.Contains(rec.Body.String(), "Index of") {
					t.Errorf("earlier cached child disclosed after selective revocation: %d %s", rec.Code, rec.Body)
				}
				if got := w.Request("GET", "/artifactory/remote/dir/z.bin", "reader", ""); got.Code != 200 {
					t.Errorf("remaining readable file refused: %d %s", got.Code, got.Body)
				}
				if got := w.Request("GET", "/artifactory/remote/dir/a.bin", "reader", ""); got.Code != 403 {
					t.Errorf("selectively revoked file remained readable: %d %s", got.Code, got.Body)
				}
			})
		}
	}
}

func TestAccRemoteFolderNarrowReadAndPatternFiltering(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := remoteFolderWorld(t, backend)
			remoteFolderCache(t, w, "dir/allowed/inside.bin", "dir/private.bin", "dir/excluded.tmp")
			w.Repo.ExcludesPattern = ptr("**/*.tmp")
			w.Update()
			w.Upstream.Set("/dir/", upstreamIndex("allowed/", "private.bin", "excluded.tmp", "live.bin"))
			if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "narrow", PasswordHash: support.CheapHash}); err != nil {
				t.Fatal(err)
			}
			if err := w.Runtime.Store.Permissions().Create(t.Context(), accesstest.Permission{
				Name: "narrow-folder", Resources: map[accessapi.ResourceType]accesstest.Resource{
					accessapi.ResourceArtifact: {
						Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"narrow": accessapi.ActionRead}},
						Targets: map[string]accesstest.Target{"remote": {Include: []string{"dir/allowed/inside.bin"}}},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			wantRemoteFolder(t, w, "GET", "/artifactory/remote/dir/", "narrow", 200, []string{"allowed/"})
			wantRemoteFolder(t, w, "HEAD", "/artifactory/remote/dir/", "narrow", 200, []string{"allowed/"})
			if rec := w.Request("GET", "/artifactory/remote/dir/private.bin", "narrow", ""); rec.Code != 403 {
				t.Fatalf("narrow reader gained file access:%d %s", rec.Code, rec.Body)
			}
			if rec := w.Request("GET", "/artifactory/remote/dir/excluded.tmp", "admin", ""); rec.Code != 404 {
				t.Fatalf("ordinary file policy changed:%d %s", rec.Code, rec.Body)
			}
			before := len(w.Upstream.Requests())
			for _, user := range []string{"denied", ""} {
				code := 403
				if user == "" {
					code = 401
				}
				wantRemoteFolder(t, w, "GET", "/artifactory/remote/dir/", user, code, nil)
			}
			if len(w.Upstream.Requests()) != before {
				t.Fatal("refused folder request contacted origin")
			}
		})
	}
}
