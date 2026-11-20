package federationhttp_test

import (
	"context"
	"errors"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	federationhttp "github.com/weaveplatform/artifactweave/internal/federation/adapters/http"
	fedinmem "github.com/weaveplatform/artifactweave/internal/federation/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitReplicationUnboundAdapterKeepsOperationOwner(t *testing.T) {
	s, ctx := federationhttp.ReplicationAPI{}, t.Context()
	for _, call := range []func() error{
		func() error {
			_, e := s.GetRepositoryReplicationConfiguration(ctx, artgen.GetRepositoryReplicationConfigurationRequestObject{})
			return e
		},
		func() error {
			_, e := s.SetRepositoryReplicationConfiguration(ctx, artgen.SetRepositoryReplicationConfigurationRequestObject{})
			return e
		},
		func() error {
			_, e := s.UpdateRepositoryReplicationConfiguration(ctx, artgen.UpdateRepositoryReplicationConfigurationRequestObject{})
			return e
		},
		func() error {
			_, e := s.DeleteRepositoryReplicationConfiguration(ctx, artgen.DeleteRepositoryReplicationConfigurationRequestObject{})
			return e
		},
		func() error {
			_, e := s.GetGlobalSystemReplicationConfiguration(ctx, artgen.GetGlobalSystemReplicationConfigurationRequestObject{})
			return e
		},
		func() error {
			_, e := s.BlockSystemReplication(ctx, artgen.BlockSystemReplicationRequestObject{})
			return e
		},
		func() error {
			_, e := s.UnblockSystemReplication(ctx, artgen.UnblockSystemReplicationRequestObject{})
			return e
		},
	} {
		if err := call(); !errors.Is(err, apierror.ErrNotImplemented) {
			t.Fatal("unbound adapter claimed implementation", err)
		}
	}
}

func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	var public *apierror.Error
	if !errors.As(err, &public) || public.Status != status {
		t.Fatalf("status error=%v, want%d", err, status)
	}
}

func TestUnitReplicationAdapterRejectsMalformedSingleTargetBodies(t *testing.T) {
	s := federationhttp.ReplicationAPI{Configurations: &replication.Service{}}
	_, err := s.SetRepositoryReplicationConfiguration(t.Context(), artgen.SetRepositoryReplicationConfigurationRequestObject{})
	requireStatus(t, err, 400)
	_, err = s.UpdateRepositoryReplicationConfiguration(t.Context(), artgen.UpdateRepositoryReplicationConfigurationRequestObject{})
	requireStatus(t, err, 400)
	for _, raw := range []string{`[]`, `[{},{}]`, `null`, `{"include":["*"]}`, `{"exclude":["*"]}`, `{"enabled":"yes"}`} {
		var body artgen.SetRepositoryReplicationConfigurationJSONRequestBody
		if err := body.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		_, err := s.SetRepositoryReplicationConfiguration(t.Context(), artgen.SetRepositoryReplicationConfigurationRequestObject{RepoKey: "local", Body: &body})
		requireStatus(t, err, 400)
	}
	for _, tc := range []struct{ key, raw string }{
		{"local", `[]`}, {"local", `{"enabled":"yes"}`}, {"enable", `[]`}, {"disable", `{"enabled":false}`}, {"enable", `{"include":4}`},
	} {
		var body artgen.UpdateRepositoryReplicationConfigurationJSONRequestBody
		if err := body.UnmarshalJSON([]byte(tc.raw)); err != nil {
			t.Fatal(err)
		}
		_, err := s.UpdateRepositoryReplicationConfiguration(t.Context(), artgen.UpdateRepositoryReplicationConfigurationRequestObject{RepoKey: tc.key, Body: &body})
		requireStatus(t, err, 400)
	}
}

func TestUnitReplicationAdapterPreservesAuthorityErrorsAndHidesFailures(t *testing.T) {
	for _, failure := range []error{kernel.ErrUnauthenticated, kernel.ErrBadCredentials, kernel.ErrForbidden, kernel.ErrPasswordExpired, kernel.ErrAPIRestricted, replication.ErrMultipleTargets, replication.ErrAdvancedConfiguration, kernel.ErrConflict, errors.New("private backend detail")} {
		service := replication.Service{Store: fedinmem.New(memtx.New()), Authorize: func(context.Context) (replication.Policy, error) { return nil, failure }}
		s := federationhttp.ReplicationAPI{Configurations: &service}
		_, err := s.GetRepositoryReplicationConfiguration(t.Context(), artgen.GetRepositoryReplicationConfigurationRequestObject{RepoKey: "local"})
		switch failure {
		case kernel.ErrUnauthenticated, kernel.ErrBadCredentials, kernel.ErrForbidden, kernel.ErrPasswordExpired, kernel.ErrAPIRestricted:
			if !errors.Is(err, failure) {
				t.Fatal("adapter replaced authentication/authorization error", err)
			}
		case replication.ErrMultipleTargets, replication.ErrAdvancedConfiguration:
			requireStatus(t, err, 501)
		case kernel.ErrConflict:
			requireStatus(t, err, 409)
		default:
			requireStatus(t, err, 500)
			if err.Error() != "internal error" {
				t.Fatal("backend details reached public error", err)
			}
		}
	}
}

type publicPolicy struct{}

func (publicPolicy) Any() bool                      { return true }
func (publicPolicy) Allows(string, repo.Class) bool { return true }
func (publicPolicy) Platform() bool                 { return true }

type localRepository struct{}

func (localRepository) ProjectRepository(_ context.Context, key string) (artifactsapi.ProjectRepository, error) {
	return artifactsapi.ProjectRepository{Key: key, Class: repo.Local}, nil
}

func TestUnitReplicationAdapterRefusesCorruptStoredTimeout(t *testing.T) {
	st := fedinmem.New(memtx.New())
	if err := st.Replications().Put(t.Context(), model.Replication{Key: "legacy", RepoKey: "local"}); err != nil {
		t.Fatal(err)
	}
	service := replication.Service{Store: st, Ownership: localRepository{}, Authorize: func(context.Context) (replication.Policy, error) { return publicPolicy{}, nil }}
	s := federationhttp.ReplicationAPI{Configurations: &service}
	_, err := s.GetRepositoryReplicationConfiguration(t.Context(), artgen.GetRepositoryReplicationConfigurationRequestObject{RepoKey: "local"})
	requireStatus(t, err, 500)
}
