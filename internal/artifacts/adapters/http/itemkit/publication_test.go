package itemkit

import (
	"context"
	"errors"
	"reflect"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitPublicationUsesGuardedCurrentIdentityAndPolicy(t *testing.T) {
	st := inmem.New()
	var order []string
	allowed := true
	policy := &accessapitest.Decider{
		GroupNames: []string{"current-group"},
		AllowsFunc: func(rt accessapi.ResourceType, c accessapi.Repo, path string, want accessapi.Actions) bool {
			return allowed && rt == accessapi.ResourceArtifact && c.Key == "libs" && path == "actual/file" && want == accessapi.ActionWrite
		},
	}
	kit := New(Deps{Access: accessapi.API{
		Projects: &accessapitest.Projects{GuardFunc: func(ctx context.Context) error {
			if !st.InTx(ctx) {
				t.Fatal("publication guard is outside owner transaction")
			}
			order = append(order, "guard")
			return nil
		}},
		Authenticator: &accessapitest.Authenticator{CurrentFunc: func(_ context.Context, p kernel.Principal) (kernel.Principal, error) {
			order = append(order, "credential")
			p.Admin, p.Groups = false, []string{"current-group"}
			return p, nil
		}},
		Authorizer: &accessapitest.Authorizer{DecideFunc: func(_ context.Context, subject accessapi.Subject, _ bool) (accessapi.Decider, error) {
			order = append(order, "policy")
			if subject.Admin || !reflect.DeepEqual(subject.Groups, []string{"current-group"}) {
				t.Fatal("current policy used old identity flags or groups")
			}
			return policy, nil
		}},
	}})
	ctx := accessapi.WithCaller(t.Context(), kernel.Principal{Name: "writer", Admin: true, Groups: []string{"old-group"}}, &accessapitest.Decider{Admin: true})
	check := kit.PublicationCheck(repo.Config{Key: "libs", Class: repo.Local})
	for _, revoked := range []bool{false, true} {
		allowed, order = !revoked, nil
		err := st.Tx(ctx, func(ctx context.Context) error { return check(ctx, "actual/file", false, accessapi.ActionWrite) })
		if revoked && !errors.Is(err, kernel.ErrForbidden) || !revoked && err != nil {
			t.Fatalf("revoked=%v publication=%v", revoked, err)
		}
		if !reflect.DeepEqual(order, []string{"guard", "credential", "policy"}) {
			t.Fatalf("publication read order: %v", order)
		}
	}
	if err := st.Tx(ctx, func(ctx context.Context) error { return check(ctx, "actual/file", true, accessapi.ActionWrite) }); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("folder check borrowed a file-only grant", err)
	}
}

func TestUnitCurrentPublicationRefusals(t *testing.T) {
	for _, mode := range []string{"no principal", "no authenticator", "no authorizer", "guard", "ended credential", "credential fault", "policy fault", "nil policy", "registry", "anonymous"} {
		t.Run(mode, func(t *testing.T) {
			ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "writer"})
			var want error = errBoom
			auth := &accessapitest.Authenticator{}
			api := accessapi.API{Authenticator: auth, Authorizer: &accessapitest.Authorizer{}}
			switch mode {
			case "no principal":
				ctx, want = t.Context(), kernel.ErrUnauthenticated
			case "no authenticator":
				api.Authenticator, want = nil, apierror.ErrNotImplemented
			case "no authorizer":
				api.Authorizer, want = nil, apierror.ErrNotImplemented
			case "guard":
				api.Projects = &accessapitest.Projects{Err: errBoom}
			case "ended credential":
				auth.CurrentFunc = func(context.Context, kernel.Principal) (kernel.Principal, error) {
					return kernel.Principal{}, kernel.ErrNotFound
				}
				want = kernel.ErrBadCredentials
			case "credential fault":
				auth.CurrentFunc = func(context.Context, kernel.Principal) (kernel.Principal, error) { return kernel.Principal{}, errBoom }
			case "policy fault", "nil policy":
				var result error
				if mode == "policy fault" {
					result = errBoom
				} else {
					want = kernel.ErrForbidden
				}
				api.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
					return nil, result //nolint:nilnil // exercise fail-closed handling of an absent policy
				}}
			case "registry":
				ctx = kernel.WithPrincipal(ctx, kernel.Principal{Name: "writer", Registry: &kernel.RegistryAccess{}})
				auth.CurrentRegistryFunc = func(context.Context, kernel.Principal) (kernel.Principal, error) { return kernel.Principal{}, errBoom }
			case "anonymous":
				ctx, want = kernel.WithPrincipal(ctx, kernel.Anonymous), kernel.ErrUnauthenticated
			}
			kit := New(Deps{Access: api})
			err := kit.PublicationCheck(repo.Config{Key: "libs"})(ctx, "file", false, accessapi.ActionWrite)
			if !errors.Is(err, want) {
				t.Fatalf("current publication: %v, want %v", err, want)
			}
		})
	}
}
