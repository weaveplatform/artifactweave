package federation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type statusOwnership func(context.Context, string) (artifactsapi.ProjectRepository, error)

func (f statusOwnership) ProjectRepository(ctx context.Context, key string) (artifactsapi.ProjectRepository, error) {
	return f(ctx, key)
}

type statusLifetimes func(context.Context, string) (string, error)

func (f statusLifetimes) RepositoryIncarnation(ctx context.Context, key string) (string, error) {
	return f(ctx, key)
}

// Status history is read under the caller's current credential, current read
// permission on the repository root and the repository's current lifetime;
// every refresh failure refuses before any history row is selected.
func TestUnitReplicationStatusRefreshesCallerRepositoryAndLifetime(t *testing.T) {
	boom := errors.New("status authority unavailable")
	for _, tc := range []struct {
		name  string
		want  error
		steps []string
	}{
		{"current", nil, []string{"guard", "credential", "policy", "owner", "lifetime"}},
		{"anonymous", kernel.ErrUnauthenticated, nil},
		{"missing projects", kernel.ErrStore, nil},
		{"missing ownership", kernel.ErrStore, nil},
		{"missing lifetimes", kernel.ErrStore, nil},
		{"guard failure", boom, []string{"guard"}},
		{"ended credential", kernel.ErrBadCredentials, []string{"guard", "credential"}},
		{"credential failure", boom, []string{"guard", "credential"}},
		{"policy failure", boom, []string{"guard", "credential", "policy"}},
		{"missing policy", kernel.ErrForbidden, []string{"guard", "credential", "policy"}},
		{"owner failure", boom, []string{"guard", "credential", "policy", "owner"}},
		{"denied read", kernel.ErrForbidden, []string{"guard", "credential", "policy", "owner"}},
		{"lifetime failure", boom, []string{"guard", "credential", "policy", "owner", "lifetime"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []string
			step := func(name string) error {
				steps = append(steps, name)
				if tc.name == name+" failure" {
					return boom
				}
				return nil
			}
			access := accessapi.API{
				Projects: &accessapitest.Projects{GuardFunc: func(context.Context) error { return step("guard") }},
				Authenticator: &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
					if err := step("credential"); err != nil {
						return kernel.Principal{}, err
					}
					if tc.name == "ended credential" {
						return kernel.Principal{}, kernel.ErrNotFound
					}
					return kernel.Principal{Name: "current"}, nil
				}},
				Authorizer: &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
					if err := step("policy"); err != nil {
						return nil, err
					}
					if tc.name == "missing policy" {
						return nil, nil
					}
					return &accessapitest.Decider{Admin: tc.name != "denied read"}, nil
				}},
			}
			deps := federation.Deps{
				Store:  inmem.New(),
				Access: access,
				Ownership: statusOwnership(func(_ context.Context, key string) (artifactsapi.ProjectRepository, error) {
					if err := step("owner"); err != nil {
						return artifactsapi.ProjectRepository{}, err
					}
					return artifactsapi.ProjectRepository{Key: key, Class: repo.Local}, nil
				}),
				Lifetimes: statusLifetimes(func(context.Context, string) (string, error) {
					if err := step("lifetime"); err != nil {
						return "", err
					}
					return "lifetime-1", nil
				}),
			}
			switch tc.name {
			case "missing projects":
				deps.Access.Projects = nil
			case "missing ownership":
				deps.Ownership = nil
			case "missing lifetimes":
				deps.Lifetimes = nil
			}
			m, err := federation.New(deps)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := m.Status()
			if err != nil {
				t.Fatal(err)
			}
			p := kernel.Principal{Name: "stale", Admin: true}
			if tc.name == "anonymous" {
				p = kernel.Anonymous
			}
			ctx := accessapi.WithCaller(t.Context(), p, &accessapitest.Decider{Admin: true})
			class, incarnation, err := reader.Authorize(ctx, "local", "")
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(steps, tc.steps) {
				t.Fatalf("err=%v want=%v steps=%v want=%v", err, tc.want, steps, tc.steps)
			}
			if tc.want == nil && (class != repo.Local || incarnation != "lifetime-1") {
				t.Fatalf("current status authority = %q %q", class, incarnation)
			}
			if tc.want != nil && (class != "" || incarnation != "") {
				t.Fatal("refused status authority disclosed repository facts")
			}
		})
	}
}
