package retention_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/retention"
)

func TestUnitRetentionConstructionDoesNoIO(t *testing.T) {
	if m, err := retention.New(retention.Deps{}); m != nil || err == nil {
		t.Fatal("constructor accepted a missing store")
	}
	if m, err := retention.New(retention.Deps{Store: struct{ retention.Store }{}}); m == nil || err != nil {
		t.Fatal("constructor called a store accessor", err)
	}
}

func TestUnitRetentionRefreshesAuthorityWithinTransaction(t *testing.T) {
	boom := errors.New("authority unavailable")
	for _, tc := range []struct {
		name  string
		want  error
		steps []string
	}{
		{name: "current", steps: []string{"guard", "credential", "policy", "owner"}},
		{name: "anonymous", want: kernel.ErrUnauthenticated},
		{name: "missing projects", want: kernel.ErrStore},
		{name: "missing authenticator", want: kernel.ErrStore},
		{name: "missing authorizer", want: kernel.ErrStore},
		{name: "guard failure", want: boom, steps: []string{"guard"}},
		{name: "ended credential", want: kernel.ErrBadCredentials, steps: []string{"guard", "credential"}},
		{name: "policy failure", want: boom, steps: []string{"guard", "credential", "policy"}},
		{name: "missing policy", want: kernel.ErrForbidden, steps: []string{"guard", "credential", "policy"}},
		{name: "denied collection", want: kernel.ErrForbidden, steps: []string{"guard", "credential", "policy"}},
		{name: "foreign owner", want: kernel.ErrForbidden, steps: []string{"guard", "credential", "policy", "owner"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := inmem.New()
			var steps []string
			api := accessapi.API{
				Projects: &accessapitest.Projects{GuardFunc: func(ctx context.Context) error {
					if !db.InTx(ctx) {
						t.Fatal("current authority did not join policy transaction")
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
					return kernel.Principal{Name: "current"}, nil
				}},
				Authorizer: &accessapitest.Authorizer{DecideFunc: func(_ context.Context, subject accessapi.Subject, _ bool) (accessapi.Decider, error) {
					steps = append(steps, "policy")
					if subject.Name != "current" || subject.Admin {
						t.Fatal("stale administrator principal reached policy")
					}
					if tc.name == "policy failure" {
						return nil, boom
					}
					if tc.name == "missing policy" {
						return nil, nil
					}
					return &accessapitest.Decider{
						HasProjectAdministrationFunc: func() bool { return tc.name != "denied collection" },
						ManagesProjectFunc: func(key string, op accessapi.ProjectOperation) bool {
							steps = append(steps, "owner")
							return key == "alpha" && op == accessapi.ProjectManage && tc.name != "foreign owner"
						},
					}, nil
				}},
			}
			switch tc.name {
			case "missing projects":
				api.Projects = nil
			case "missing authenticator":
				api.Authenticator = nil
			case "missing authorizer":
				api.Authorizer = nil
			}
			m, err := retention.New(retention.Deps{Store: db, Access: api})
			if err != nil {
				t.Fatal(err)
			}
			p := kernel.Principal{Name: "stale", Admin: true, Anonymous: tc.name == "anonymous"}
			_, err = m.Policies().List(kernel.WithPrincipal(t.Context(), p), "alpha")
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(steps, tc.steps) {
				t.Fatalf("error=%v want=%v steps=%v want=%v", err, tc.want, steps, tc.steps)
			}
		})
	}
}
