package npmhttp_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// A metadata storage failure must not turn into an unrelated direct-file read.
func TestAccNPMVirtualMetadataFailureRefusesTarballFallback(t *testing.T) {
	for _, failure := range []string{"find", "versions"} {
		f := newFixture(t, func(st pkgroute.Store) pkgroute.Store { return failingStore{Store: st, failing: failure} })
		f.deployRich(t)
		if err := f.db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"npm-local"}})); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"rich", "rich/-/rich-1.0.0.tgz"} {
			rec := f.do("GET", "/artifactory/api/npm/npm-virtual/"+path, "reader")
			wantStatus(t, rec, 500)
		}
	}
}

type virtualBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (b virtualBackend) Read(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
	return b(ctx, q, p)
}

func TestAccNPMVirtualFinalAuthorityIsAMemberMiss(t *testing.T) {
	for _, tc := range []struct {
		name            string
		failure         error
		direct, virtual int
	}{{"denied", source.ErrDenied, 403, 404}, {"missing", kernel.ErrNotFound, 404, 404}, {"storage", fmt.Errorf("local copy: %w", format.ErrStorageFault), 500, 500}} {
		for _, key := range []string{"npm-remote", "npm-virtual"} {
			t.Run(tc.name+"-"+key, func(t *testing.T) {
				f := newRemoteFixture(t)
				f.packageDoc(t, "pkg", "1.0.0")
				if err := f.db.Repos().
					Create(t.Context(), *repo.Defaults(&repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"npm-remote"}})); err != nil {
					t.Fatal(err)
				}
				original := f.svc.HTTP
				checks := 0
				f.svc.HTTP = virtualBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
					result, err := original.Read(ctx, q, p)
					if err == nil && q.Resource.Kind == source.ResourceKindArtifact {
						previous := result.Revalidate
						result.Revalidate = func(ctx context.Context) error {
							checks++
							if checks >= 2 {
								return tc.failure
							}
							if previous != nil {
								return previous(ctx)
							}
							return nil
						}
					}
					return result, err
				})
				response := f.do("GET", "/artifactory/api/npm/"+key+"/pkg/-/pkg-1.0.0.tgz", "reader")
				want := tc.direct
				if key == "npm-virtual" {
					want = tc.virtual
				}
				wantStatus(t, response, want)
				if checks < 2 {
					t.Fatal("did not reach the final source fence", checks)
				}
				if key == "npm-virtual" && response.Header().Get("WWW-Authenticate") != "" {
					t.Fatal(response.Header())
				}
			})
		}
	}
}
