package federation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitFederationConstructionDoesNoIO(t *testing.T) {
	if m, err := federation.New(federation.Deps{}); m != nil || err == nil {
		t.Fatal("constructor accepted missing store")
	}
	if m, err := federation.New(federation.Deps{Store: struct{ federation.Store }{}}); m == nil || err != nil {
		t.Fatal("constructor accessed dependencies", err)
	}
}

func TestUnitReplicationRefreshesAuthorityBeforeGlobalAccess(t *testing.T) {
	boom := errors.New("authority unavailable")
	for _, tc := range []struct {
		name  string
		want  error
		steps []string
	}{
		{"current", nil, []string{"guard", "credential", "policy"}},
		{"anonymous", kernel.ErrUnauthenticated, nil},
		{"missing projects", kernel.ErrStore, nil},
		{"missing authenticator", kernel.ErrStore, nil},
		{"missing authorizer", kernel.ErrStore, nil},
		{"guard failure", boom, []string{"guard"}},
		{"ended credential", kernel.ErrBadCredentials, []string{"guard", "credential"}},
		{"credential failure", boom, []string{"guard", "credential"}},
		{"policy failure", boom, []string{"guard", "credential", "policy"}},
		{"missing policy", kernel.ErrForbidden, []string{"guard", "credential", "policy"}},
		{"denied policy", kernel.ErrForbidden, []string{"guard", "credential", "policy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := inmem.New()
			var steps []string
			access := accessapi.API{
				Projects: &accessapitest.Projects{GuardFunc: func(ctx context.Context) error {
					if !st.InTx(ctx) {
						t.Fatal("guard outside replication transaction")
					}
					steps = append(steps, "guard")
					if tc.name == "guard failure" {
						return boom
					}
					return nil
				}},
				Authenticator: &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
					steps = append(steps, "credential")
					if tc.name == "ended credential" {
						return kernel.Principal{}, kernel.ErrNotFound
					}
					if tc.name == "credential failure" {
						return kernel.Principal{}, boom
					}
					return kernel.Principal{Name: "current"}, nil
				}},
				Authorizer: &accessapitest.Authorizer{DecideFunc: func(_ context.Context, subject accessapi.Subject, _ bool) (accessapi.Decider, error) {
					steps = append(steps, "policy")
					if subject.Name != "current" || subject.Admin {
						t.Fatal("stale identity supplied to policy")
					}
					if tc.name == "policy failure" {
						return nil, boom
					}
					if tc.name == "missing policy" {
						return nil, nil
					}
					return &accessapitest.Decider{Admin: tc.name != "denied policy"}, nil
				}},
			}
			switch tc.name {
			case "missing projects":
				access.Projects = nil
			case "missing authenticator":
				access.Authenticator = nil
			case "missing authorizer":
				access.Authorizer = nil
			}
			m, err := federation.New(federation.Deps{Store: st, Access: access})
			if err != nil {
				t.Fatal(err)
			}
			p := kernel.Principal{Name: "stale", Admin: true}
			if tc.name == "anonymous" {
				p = kernel.Anonymous
			}
			ctx := accessapi.WithCaller(t.Context(), p, &accessapitest.Decider{Admin: true})
			_, err = m.Configurations().Global(ctx)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(steps, tc.steps) {
				t.Fatalf("err=%v want=%v steps=%v want=%v", err, tc.want, steps, tc.steps)
			}
		})
	}
}
