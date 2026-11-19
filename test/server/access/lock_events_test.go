package access_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccAccountLockProducerPreservesAuthenticationRefusals(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			rt := harness.App(t, func(cfg *config.Config) {
				cfg.Bootstrap.AdminPassword = "pw"
				if backend == "postgres" {
					cfg.Database = config.Database{Driver: "postgres", DSN: sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))}
				}
			})
			ctx := t.Context()
			if err := rt.Store.Users().Create(ctx, accesstest.User{Name: "event-user", PasswordHash: support.CheapHash}); err != nil {
				t.Fatal(err)
			}
			if err := rt.Store.Settings().Put(ctx, settings.SettingUserLockPolicy, model.UserLockPolicy{Enabled: true, LoginAttempts: 2}); err != nil {
				t.Fatal(err)
			}
			rt.Auth.ForgetPolicies()
			origin, err := instance.EnsureID(ctx, rt.Store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rt.Store.Events().RegisterConsumer(ctx, origin, "proof"); err != nil {
				t.Fatal(err)
			}
			h := harness.Router(harness.Config{Store: rt.Store, Auth: rt.Auth, Tokens: rt.Tokens, Blobs: rt.Blobs, Keys: rt.Keys})
			for _, attempt := range []struct {
				password string
				status   int
			}{{"wrong", http.StatusUnauthorized}, {"wrong", http.StatusUnauthorized}, {"pw", http.StatusForbidden}} {
				request := httptest.NewRequest(http.MethodGet, "/artifactory/api/security/users", nil)
				request.SetBasicAuth("EVENT-USER", attempt.password)
				response := httptest.NewRecorder()
				h.ServeHTTP(response, request)
				if response.Code != attempt.status {
					t.Fatalf("authentication refusal=%d %s", response.Code, response.Body)
				}
			}
			events, _, err := rt.Store.Events().Next(ctx, origin, "proof", 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Topic != api.UserLocked {
				t.Fatalf("lock events=%+v", events)
			}
			var facts api.UserLockedEvent
			if err := json.Unmarshal(events[0].Payload, &facts); err != nil {
				t.Fatal(err)
			}
			if facts.User != "event-user" || facts.Version != 1 || facts.Outcome != "committed" {
				t.Fatalf("lock facts=%+v", facts)
			}
		})
	}
}
