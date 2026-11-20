package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/model"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	webhooksvc "github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type subscriptionPolicy struct{ allowed bool }

func (p subscriptionPolicy) Any() bool { return p.allowed }

func (p subscriptionPolicy) Allows(project, domain string) bool {
	return p.allowed && project == "alpha" && domain == "artifact"
}

// RunSubscriptionAdministration checks the authorization/publication boundary
// over each real store. Access resolves the policy; notifications must enforce
// it before inspection and retain it through secret-safe CRUD in the same Tx.
func RunSubscriptionAdministration(t *testing.T, newStore Factory) {
	t.Helper()
	st, ctx := newStore(t), t.Context()
	allowed := true
	a := webhooksvc.Administration{
		Service: webhooksvc.Service{Store: st},
		Authorize: func(ctx context.Context, _ kernel.ScopeActions) (webhooksvc.Policy, error) {
			transaction, ok := st.(interface{ InTx(context.Context) bool })
			if !ok || !transaction.InTx(ctx) {
				t.Fatal("current authorization is outside subscription publication transaction")
			}
			return subscriptionPolicy{allowed: allowed}, nil
		},
	}
	in := secretInput()
	in.ProjectKey = stringPointer("alpha")
	created := storekit.Get[webhooksvc.View](t)(a.Create(ctx, in))
	safeSecretView(t, created)
	if !created.Handlers[0].SecretConfigured {
		t.Fatal("authorized create lost configured-secret state")
	}
	_, err := a.Create(ctx, in)
	storekit.WantErr(t, err, kernel.ErrConflict)
	other := in
	other.Key, other.ProjectKey = "other", stringPointer("beta")
	_, err = a.Create(ctx, other)
	storekit.WantErr(t, err, kernel.ErrForbidden)
	_, err = st.Webhooks().Get(ctx, other.Key)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.Get[webhooksvc.View](t)(a.Service.Create(ctx, other))
	view, err := a.Get(ctx, other.Key)
	storekit.WantErr(t, err, kernel.ErrForbidden)
	storekit.Same(t, "denied get returns no projection", webhooksvc.View{}, view)
	storekit.WantErr(t, a.Delete(ctx, other.Key), kernel.ErrForbidden)
	list := storekit.Get[[]webhooksvc.View](t)(a.List(ctx, port.WebhookFilter{}))
	if len(list) != 1 || list[0].Key != in.Key {
		t.Fatalf("project list disclosed another project: %+v", list)
	}
	safeSecretView(t, list)

	in.Key, in.ProjectKey, in.Handlers[0].Secret = "", nil, nil
	in.Enabled, in.Description = false, "metadata update"
	storekit.Must(t, a.Update(ctx, created.Key, in))
	stored := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, created.Key))
	if stored.ProjectKey != "alpha" || stored.Handlers[0].Secret != plainFixture || stored.Description != in.Description || stored.Enabled {
		t.Fatal("authorized metadata update lost immutable scope or retained secret")
	}
	for name, change := range map[string]func(*webhooksvc.Input){
		"move project": func(in *webhooksvc.Input) { in.ProjectKey = stringPointer("beta") },
		"change key":   func(in *webhooksvc.Input) { in.Key = "different" },
		"wrong event":  func(in *webhooksvc.Input) { in.EventTypes = []string{"pushed"} },
		"bad URL":      func(in *webhooksvc.Input) { in.Handlers[0].URL = "file:///tmp/receiver" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := kernel.Clone(in)
			bad.Handlers[0].Secret = stringPointer("")
			change(&bad)
			storekit.WantErr(t, a.Update(ctx, created.Key, bad), kernel.ErrInvalid)
			got := storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, created.Key))
			if !reflect.DeepEqual(stored, got) {
				t.Fatal("invalid replacement changed subscription or secrets")
			}
		})
	}
	foreignDomain := kernel.Clone(in)
	foreignDomain.Domain, foreignDomain.EventTypes = model.DomainBuild, []string{"uploaded"}
	storekit.WantErr(t, a.Update(ctx, created.Key, foreignDomain), kernel.ErrForbidden)
	empty := storekit.Get[[]webhooksvc.View](t)(a.List(ctx, port.WebhookFilter{EnabledOnly: true}))
	if empty == nil || len(empty) != 0 {
		t.Fatal("empty authorized list is not an empty array")
	}
	allowed = false
	if list, err := a.List(ctx, port.WebhookFilter{ProjectKey: "nonexistent"}); list != nil || !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("caller filter bypassed authorization: %+v %v", list, err)
	}
	storekit.WantErr(t, a.Delete(ctx, created.Key), kernel.ErrForbidden)
	storekit.Get[model.WebhookSubscription](t)(st.Webhooks().Get(ctx, created.Key))
	allowed = true
	storekit.Must(t, a.Delete(ctx, created.Key))
	_, err = a.Get(ctx, created.Key)
	storekit.WantErr(t, err, kernel.ErrNotFound)
}
