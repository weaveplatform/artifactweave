package notifications_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitNotificationsConstructionDoesNoIO(t *testing.T) {
	if m, err := notifications.New(notifications.Deps{}); m != nil || err == nil {
		t.Fatal("constructor accepted a missing store")
	}
	// Every promoted method would panic: construction must call none of them.
	if m, err := notifications.New(notifications.Deps{Store: struct{ notifications.Store }{}}); m == nil || err != nil {
		t.Fatal("constructor performed store I/O", err)
	}
}

func TestUnitNotificationsRefreshAuthorityBeforeSubscriptionAccess(t *testing.T) {
	boom := errors.New("authority unavailable")
	for _, tc := range []struct {
		name  string
		want  error
		steps []string
	}{
		{name: "current", steps: []string{"guard", "credential", "policy"}},
		{name: "anonymous", want: kernel.ErrUnauthenticated},
		{name: "missing projects", want: apierror.ErrNotImplemented},
		{name: "missing authenticator", want: apierror.ErrNotImplemented},
		{name: "missing authorizer", want: apierror.ErrNotImplemented},
		{name: "guard failure", want: boom, steps: []string{"guard"}},
		{name: "ended credential", want: kernel.ErrBadCredentials, steps: []string{"guard", "credential"}},
		{name: "credential failure", want: boom, steps: []string{"guard", "credential"}},
		{name: "policy failure", want: boom, steps: []string{"guard", "credential", "policy"}},
		{name: "missing policy", want: kernel.ErrForbidden, steps: []string{"guard", "credential", "policy"}},
		{name: "denied policy", want: kernel.ErrForbidden, steps: []string{"guard", "credential", "policy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := inmem.New()
			var steps []string
			access := accessapi.API{
				Projects: &accessapitest.Projects{GuardFunc: func(ctx context.Context) error {
					if !st.InTx(ctx) {
						t.Fatal("authority guard does not join subscription transaction")
					}
					steps = append(steps, "guard")
					if tc.name == "guard failure" {
						return boom
					}
					return nil
				}},
				Authenticator: &accessapitest.Authenticator{CurrentFunc: func(_ context.Context, _ kernel.Principal) (kernel.Principal, error) {
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
						t.Fatal("policy used the request's stale identity")
					}
					if tc.name == "policy failure" {
						return nil, boom
					}
					if tc.name == "missing policy" {
						return nil, nil
					}
					return &accessapitest.Decider{HasWebhookAccessFunc: func(url string, action kernel.ScopeActions) bool {
						if url != "https://configured.example" || action != kernel.ScopeRead {
							t.Fatal("policy used another platform URL or action")
						}
						return tc.name != "denied policy"
					}}, nil
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
			m, err := notifications.New(notifications.Deps{Store: st, Access: access, ServiceURL: "https://configured.example"})
			if err != nil {
				t.Fatal(err)
			}
			p := kernel.Principal{Name: "stale", Admin: true}
			if tc.name == "anonymous" {
				p = kernel.Anonymous
			}
			ctx := accessapi.WithCaller(t.Context(), p, &accessapitest.Decider{Admin: true})
			got, err := m.Subscriptions().List(ctx, port.WebhookFilter{})
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(steps, tc.steps) {
				t.Fatalf("err=%v want=%v steps=%v want=%v", err, tc.want, steps, tc.steps)
			}
			if err == nil && got == nil || err != nil && got != nil {
				t.Fatal("list published an invalid or partial result")
			}
		})
	}
}
