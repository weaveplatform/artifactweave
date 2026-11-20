package artifactshttp

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	golanghttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/golang/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

type publicationEnd func()

func (end publicationEnd) Read([]byte) (int, error) {
	end()
	return 0, io.EOF
}

func TestAccProjectPublicationRechecksRolesGroupsAndOwnershipAfterStreaming(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Generic, repo.Go} {
		for _, change := range []string{"unchanged", "role revoked", "membership removed", "group removed", "repository moved"} {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				st, ctx := inmem.New(), t.Context()
				cheapSeed(t, st)
				for _, key := range []string{"alpha", "beta"} {
					if err := st.Projects().Create(ctx, accesstest.Project{Key: key}); err != nil {
						t.Fatal(err)
					}
				}
				if err := st.Groups().Create(ctx, accesstest.Group{Name: "publishers"}); err != nil {
					t.Fatal(err)
				}
				u, err := st.Users().Get(ctx, "admin")
				if err != nil {
					t.Fatal(err)
				}
				u.Admin, u.Groups = false, []string{"publishers"}
				if err := st.Users().Update(ctx, u); err != nil {
					t.Fatal(err)
				}
				role := accesstest.Role{
					ProjectKey: "alpha", Name: "publisher", Type: accesstest.RoleCustom,
					Actions: []string{"DEPLOY_CACHE_REPOSITORY"}, Environments: []string{"DEV"},
				}
				if err := st.Projects().PutRole(ctx, role); err != nil {
					t.Fatal(err)
				}
				if err := st.Projects().PutMember(ctx, accesstest.ProjectMember{
					ProjectKey: "alpha", Type: accesstest.PrincipalGroup, Name: "publishers", Roles: []string{"publisher"},
				}); err != nil {
					t.Fatal(err)
				}
				c := *repo.Defaults(&repo.Config{Key: "libs", Class: repo.Local, PackageType: kind, ProjectKey: "alpha"})
				if err := st.Repos().Create(ctx, c); err != nil {
					t.Fatal(err)
				}
				access := accesstest.Over(t, st, accesstest.WithProjectRepositories(repos.NewProjectRepositoryReader(st))).API()
				p, err := access.Authenticator.Basic(ctx, "admin", "pw")
				if err != nil {
					t.Fatal(err)
				}
				p, cached, err := access.Authorizer.Authorize(ctx, p, false)
				if err != nil || cached.IsAdmin() {
					t.Fatal("project publisher must not inherit platform administration", err)
				}
				ctx = accessapi.WithCaller(ctx, p, cached)
				blobs, err := fs.New(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				service := newArtifactoryAPI(Config{Store: st, Blobs: blobs, Access: access})
				router := chi.NewRouter()
				service.RegisterItemRoutes(router)
				payload, storedPath := "payload", "nested/file.bin"
				requestPath := "/artifactory/libs/" + storedPath
				if kind == repo.Go {
					pkgroute.Mount(router, service.ProtocolOptions(), golanghttp.Protocol())
					payload, storedPath = `{"Version":"v1.0.0","Time":"2026-11-20T00:00:00Z"}`, "example.com/module/@v/v1.0.0.info"
					requestPath = "/artifactory/api/go/libs/" + storedPath
				}
				streamed := false
				body := io.MultiReader(strings.NewReader(payload), publicationEnd(func() {
					if st.InTx(ctx) {
						t.Fatal("client streaming holds a metadata transaction")
					}
					streamed = true
					var err error
					switch change {
					case "role revoked":
						role.Actions = nil
						err = st.Projects().PutRole(ctx, role)
					case "membership removed":
						err = st.Projects().DeleteMember(ctx, "alpha", accesstest.PrincipalGroup, "publishers")
					case "group removed":
						u.Groups = nil
						err = st.Users().Update(ctx, u)
					case "repository moved":
						c.ProjectKey = "beta"
						err = st.Repos().Update(ctx, c)
					}
					if err != nil {
						t.Fatal(err)
					}
				}))
				r := httptest.NewRequestWithContext(ctx, http.MethodPut, requestPath, body)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				want := http.StatusForbidden
				if change == "unchanged" {
					want = http.StatusCreated
				}
				if !streamed || w.Code != want {
					t.Fatalf("streamed=%v status=%d body=%s want=%d", streamed, w.Code, w.Body.String(), want)
				}
				_, err = st.Items().Get(ctx, c.Key, false, storedPath)
				if change == "unchanged" && err != nil || change != "unchanged" && !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("item after project authority change: %v", err)
				}
			})
		}
	}
}

func TestAccDeployRechecksCurrentAuthorityAfterClientStreaming(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    repo.PackageType
		revoke  bool
		explode string
	}{
		{name: "generic/current", kind: repo.Generic},
		{name: "generic/revoked", kind: repo.Generic, revoke: true},
		{name: "go/current", kind: repo.Go},
		{name: "go/revoked", kind: repo.Go, revoke: true},
		{name: "archive/current", kind: repo.Generic, explode: "X-Explode-Archive"},
		{name: "archive/revoked", kind: repo.Generic, revoke: true, explode: "X-Explode-Archive"},
		{name: "atomic archive/current", kind: repo.Generic, explode: "X-Explode-Archive-Atomic"},
		{name: "atomic archive/revoked", kind: repo.Generic, revoke: true, explode: "X-Explode-Archive-Atomic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, ctx := inmem.New(), t.Context()
			cheapSeed(t, st)
			c := *repo.Defaults(&repo.Config{Key: "libs", Class: repo.Local, PackageType: tc.kind})
			if err := st.Repos().Create(ctx, c); err != nil {
				t.Fatal(err)
			}
			blobs, err := fs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			access := accesstest.Over(t, st).API()
			p, err := access.Authenticator.Basic(ctx, "admin", "pw")
			if err != nil {
				t.Fatal(err)
			}
			p, cached, err := access.Authorizer.Authorize(ctx, p, false)
			if err != nil || !cached.IsAdmin() {
				t.Fatal("initial administrator admission", err)
			}
			ctx = accessapi.WithCaller(ctx, p, cached)
			service := newArtifactoryAPI(Config{Store: st, Blobs: blobs, Access: access})
			router := chi.NewRouter()
			service.RegisterItemRoutes(router)
			payload, storedPath := "payload", "nested/file.bin"
			requestPath := "/artifactory/libs/" + storedPath
			if tc.kind == repo.Go {
				pkgroute.Mount(router, service.ProtocolOptions(), golanghttp.Protocol())
				payload = `{"Version":"v1.0.0","Time":"2026-11-20T00:00:00Z"}`
				storedPath = "example.com/module/@v/v1.0.0.info"
				requestPath = "/artifactory/api/go/libs/" + storedPath
			}
			if tc.explode != "" {
				var archive bytes.Buffer
				writer := zip.NewWriter(&archive)
				file, err := writer.Create("inside.txt")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(file, "payload"); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				payload, storedPath = archive.String(), "nested/inside.txt"
				requestPath = "/artifactory/libs/nested/archive.zip"
			}
			streamed := false
			body := io.MultiReader(strings.NewReader(payload), publicationEnd(func() {
				streamed = true
				if st.InTx(ctx) {
					t.Fatal("client body was streamed inside a metadata transaction")
				}
				if tc.revoke {
					u, err := st.Users().Get(ctx, p.Name)
					if err != nil {
						t.Fatal(err)
					}
					u.Admin, u.Groups = false, nil
					if err := st.Users().Update(ctx, u); err != nil {
						t.Fatal(err)
					}
				}
			}))
			r := httptest.NewRequestWithContext(ctx, http.MethodPut, requestPath, body)
			if tc.explode != "" {
				r.Header.Set(tc.explode, "true")
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			want := http.StatusCreated
			if tc.revoke {
				want = http.StatusForbidden
			}
			if !streamed || w.Code != want {
				t.Fatalf("streamed=%v, status=%d body=%s want=%d", streamed, w.Code, w.Body.String(), want)
			}
			_, err = st.Items().Get(ctx, c.Key, false, storedPath)
			if tc.revoke && !errors.Is(err, kernel.ErrNotFound) || !tc.revoke && err != nil {
				t.Fatalf("publication state after current authorization: %v", err)
			}
		})
	}
}
