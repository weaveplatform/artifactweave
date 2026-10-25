package docker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A token from an OIDC exchange may name a user who is not stored (research
// 21). The registry token it obtains stands for the same user (decision
// 0005): a transient user pulls with the auto-join groups' permissions, a
// synthetic subject with its scope's groups alone, and neither token
// reaches an image it does not grant nor a route outside the registry.
func TestAccRegistryTokenForOIDCUsers(t *testing.T) {
	ctx := context.Background()
	exchanged := map[string]string{}
	var tokens *token.Service
	h := statusFixture(t, false, func(c *Config) {
		db := c.Store
		tokens = c.Tokens
		if err := db.Groups().Create(ctx, types.Group{Name: "ci", AutoJoin: true}); err != nil {
			t.Fatal(err)
		}
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: "ci-pull",
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Groups: map[string]types.Actions{"ci": types.ActionRead}},
				Targets: map[string]types.Target{"docker-blobs": {Include: []string{"app/**"}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
			Hash: testHash, Tokens: c.Tokens, Groups: db.Groups(),
		})
		if err != nil {
			t.Fatal(err)
		}
		c.Auth = a
		groups, err := types.ParseScope("applied-permissions/groups:ci")
		if err != nil {
			t.Fatal(err)
		}
		hour := time.Hour
		for name, r := range map[string]token.Request{
			"transient": {Username: "ci-robot", Identity: types.IdentityTransient},
			"synthetic": {
				Username: "oidc-github-ci", Scope: groups, Identity: types.IdentitySynthetic,
			},
		} {
			// Revocable, as an exchanged token above the revocable threshold is.
			r.ExpiresIn, r.ByAdmin, r.ForceRevocable = &hour, true, true
			issued, err := c.Tokens.Issue(ctx, r)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			exchanged[name] = issued.AccessToken
		}
	})
	for name, exchange := range exchanged {
		// A token for app alone: another image needs a token of its own,
		// whatever the user may read there (the seeded readers group is
		// auto-join, so a transient user reads everything).
		rec := do(t, h, http.MethodGet, "/v2/token?scope=repository:docker-blobs/app:pull",
			bearerAuth(exchange), "")
		var body struct {
			Token string `json:"token"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
			body.Token == "" {
			t.Fatalf("%s: token: %d %s", name, rec.Code, rec.Body)
		}
		registry := bearerAuth(body.Token)
		if rec := do(
			t,
			h,
			http.MethodGet,
			"/v2/docker-blobs/app/manifests/v1",
			registry,
			"",
		); rec.Code != http.StatusOK {
			t.Errorf("%s: pull the granted image: %d %s", name, rec.Code, rec.Body)
		}
		rec = do(t, h, http.MethodGet, "/v2/docker-blobs/other/manifests/v1", registry, "")
		if rec.Code != http.StatusUnauthorized ||
			!strings.Contains(rec.Header().Get("WWW-Authenticate"), "docker-blobs/other:pull") {
			t.Errorf("%s: another image: %d %v", name, rec.Code, rec.Header())
		}
		if rec := do(
			t,
			h,
			http.MethodGet,
			"/artifactory/api/repositories",
			registry,
			"",
		); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: outside the registry: %d", name, rec.Code)
		}
		// Revoking the exchanged token ends the registry token at once,
		// although nothing stores the registry token itself.
		if err := tokens.RevokeValue(ctx, exchange); err != nil {
			t.Fatal(err)
		}
		if rec := do(
			t,
			h,
			http.MethodGet,
			"/v2/docker-blobs/app/manifests/v1",
			registry,
			"",
		); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: after revoking the exchanged token: %d", name, rec.Code)
		}
	}
	if len(exchanged) != 2 {
		t.Fatalf("issued %d exchanged tokens", len(exchanged))
	}
}
