package authz

import (
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

const webhookPlatform = "https://platform.example"

func TestUnitWebhookManagementUsesCurrentPlatformRolesAndScopeCeilings(t *testing.T) {
	db, src := projectWorld(t)
	src.Users = db.Users()
	u, err := db.Users().Get(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	u.Roles.ManageWebhook = true
	if err := db.Users().Update(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{name: "password role", want: true},
		{name: "identity token role", raw: "applied-permissions/user", want: true},
		{name: "artifact ceiling", raw: "artifact:*:r"},
		{name: "mixed identity artifact ceiling", raw: "applied-permissions/user artifact:*:r"},
		{name: "build ceiling", raw: "applied-permissions/user build:*:r"},
		{name: "repository config ceiling", raw: "applied-permissions/user repo:*:r"},
		{name: "role not selected by group scope", raw: "applied-permissions/groups:team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := accessapi.Subject{Name: "alice", Groups: []string{"team"}}
			if tc.raw != "" {
				s.Scope = scope(t, tc.raw)
			}
			d := projectDecider(t, src, s)
			if d.HasWebhookAccess(webhookPlatform, kernel.ScopeRead) != tc.want || d.ManagesWebhook("beta", "user", webhookPlatform, kernel.ScopeWrite) != tc.want {
				t.Fatal("platform management ignored current role selection or token ceiling")
			}
			if d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: "alpha-dev"}, "file", accessapi.ActionRead) {
				t.Fatal("webhook management manufactured content Read")
			}
		})
	}
	before := projectDecider(t, src, accessapi.Subject{Name: "alice"})
	u.Roles.ManageWebhook = false
	if err := db.Users().Update(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	if projectDecider(t, src, accessapi.Subject{Name: "alice"}).HasWebhookAccess(webhookPlatform, kernel.ScopeRead) || !before.HasWebhookAccess(webhookPlatform, kernel.ScopeRead) {
		t.Fatal("role revocation failed or mutated an existing immutable snapshot")
	}
	g, err := db.Groups().Get(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	g.Roles.ManageWebhook = true
	if err := db.Groups().Update(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"applied-permissions/user", "applied-permissions/groups:team"} {
		d := projectDecider(t, src, accessapi.Subject{Name: "alice", Groups: []string{"team"}, Scope: scope(t, raw)})
		if !d.ManagesWebhook("", "curation", webhookPlatform, kernel.ScopeDelete) {
			t.Fatal("current selected group's Manage Webhooks role was lost")
		}
	}
	if projectDecider(t, src, accessapi.Subject{Name: "alice"}).HasWebhookAccess(webhookPlatform, kernel.ScopeRead) {
		t.Fatal("unselected group granted webhook management")
	}
}

func TestUnitWebhookProjectAdministratorIsLimitedToOwnFourDomains(t *testing.T) {
	db, src := projectWorld(t)
	projectMember(t, db, model.PrincipalUser, "alice", "Project Admin")
	d := projectDecider(t, src, accessapi.Subject{Name: "alice"})
	if d.IsAdmin() || !d.HasWebhookAccess(webhookPlatform, kernel.ScopeRead) {
		t.Fatal("project administrator must pass the collection gate without becoming platform administrator")
	}
	for _, domain := range []string{"artifact", "artifact_property", "docker", "build", "user", "curation", "release_bundle_v2"} {
		for _, key := range []string{"alpha", "beta", ""} {
			want := key == "alpha" && (domain == "artifact" || domain == "artifact_property" || domain == "docker" || domain == "build")
			if d.ManagesWebhook(key, domain, webhookPlatform, kernel.ScopeWrite) != want {
				t.Fatalf("project administrator escaped boundary: project=%q domain=%s", key, domain)
			}
		}
	}
	if err := db.Projects().DeleteMember(t.Context(), "alpha", model.PrincipalUser, "alice"); err != nil {
		t.Fatal(err)
	}
	if projectDecider(t, src, accessapi.Subject{Name: "alice"}).HasWebhookAccess(webhookPlatform, kernel.ScopeRead) {
		t.Fatal("removed project administrator still passes the collection gate")
	}
}

func TestUnitWebhookScopesNameThePlatformAndCurrentProjectLifetime(t *testing.T) {
	db, src := projectWorld(t)
	authority, err := db.Projects().Authority(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw, key, url string
		action              kernel.ScopeActions
		want                bool
	}{
		{name: "qualified read", raw: "system:webhooks@alpha/https://platform.example:r", key: "alpha", url: webhookPlatform, action: kernel.ScopeRead, want: true},
		{name: "qualified future domain", raw: "system:webhooks@alpha/https://platform.example:w", key: "alpha", url: webhookPlatform, action: kernel.ScopeWrite, want: true},
		{name: "other project", raw: "system:webhooks@alpha/https://platform.example:r", key: "beta", url: webhookPlatform, action: kernel.ScopeRead},
		{name: "wrong service URL", raw: "system:webhooks@alpha/https://receiver.example:r", key: "alpha", url: webhookPlatform, action: kernel.ScopeRead},
		{name: "missing configured URL", raw: "system:webhooks@alpha/https://platform.example:r", key: "alpha", action: kernel.ScopeRead},
		{name: "read cannot write", raw: "system:webhooks@alpha/https://platform.example:r", key: "alpha", url: webhookPlatform, action: kernel.ScopeWrite},
		{name: "explicit service-wide capability", raw: "system:webhooks:r,w,d", key: "beta", url: webhookPlatform, action: kernel.ScopeDelete, want: true},
		{name: "no requested action", raw: "system:webhooks:r,w,d", key: "alpha", url: webhookPlatform},
		{name: "unrelated system capability", raw: "system:info:r", key: "alpha", url: webhookPlatform, action: kernel.ScopeRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := accessapi.Subject{Name: "visitor", Scope: scope(t, tc.raw), ProjectIncarnations: map[string]string{"alpha": string(authority.Incarnation)}}
			d := projectDecider(t, src, s)
			if d.ManagesWebhook(tc.key, "curation", tc.url, tc.action) != tc.want {
				t.Fatal("scope ignored its project/platform/action boundary")
			}
			if tc.want && !d.HasWebhookAccess(tc.url, tc.action) {
				t.Fatal("valid explicit webhook capability cannot list its collection")
			}
		})
	}
	s := accessapi.Subject{
		Name:                "admin",
		Admin:               true,
		ProjectKey:          "alpha",
		ProjectIncarnations: map[string]string{"alpha": string(authority.Incarnation)},
		Scope:               scope(t, "system:webhooks:r,w,d"),
	}
	d := projectDecider(t, src, s)
	if !d.ManagesWebhook("alpha", "user", webhookPlatform, kernel.ScopeRead) || d.ManagesWebhook("beta", "user", webhookPlatform, kernel.ScopeRead) ||
		d.ManagesWebhook("", "user", webhookPlatform, kernel.ScopeRead) {
		t.Fatal("explicit webhook capability escaped the credential's project boundary")
	}
}
