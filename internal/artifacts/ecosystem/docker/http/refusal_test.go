package dockerhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// refusalBody is the OCI envelope of 7.x's refusal with message.
func refusalBody(message string) string {
	return `{"errors":[{"code":"UNAUTHORIZED","message":"` + message + `"}]}` + "\n"
}

// pullRefusal is 7.x's refused pull of what, at repo:path, by user
// (RTFACT-27228).
func pullRefusal(what, repoPath, user string) string {
	return "The client does not have permission for " + what +
		": Download request for repo:path '" + repoPath + "' is forbidden for user: '" + user + "'."
}

// TestUnitRefusalMessages: a caller the permissions refuse gets 403 with
// UNAUTHORIZED and 7.x's message (#1276): a pull names what it reads, the
// repository (a remote's cache) and the user; a push and a delete name
// neither. No refusal carries a challenge.
func TestUnitRefusalMessages(t *testing.T) {
	hex := strings.Repeat("a", 64)
	local := repo.Config{Key: "docker-local", Class: repo.Local}
	remote := repo.Config{Key: "hub", Class: repo.Remote}
	user := kernel.Principal{Name: "reader"}
	push := "The client does not have permission to push to the repository."
	for _, c := range []struct {
		name      string
		kind      Kind
		repo      repo.Config
		reference string
		method    string
		p         kernel.Principal
		message   string
	}{
		{
			"manifest by tag", KindManifest, local, "v1", http.MethodGet, user,
			pullRefusal("manifest", "docker-local:team/app/v1/manifest.json", "reader"),
		},
		{
			"manifest HEAD", KindManifest, local, "v1", http.MethodHead, user,
			pullRefusal("manifest", "docker-local:team/app/v1/manifest.json", "reader"),
		},
		{
			"manifest by digest in a local repository", KindManifest, local, "sha256:" + hex,
			http.MethodGet, user,
			pullRefusal("manifest", "docker-local:team/app/sha256:"+hex+"/manifest.json", "reader"),
		},
		{
			"manifest by digest in a remote's cache", KindManifest, remote, "sha256:" + hex,
			http.MethodGet, user,
			pullRefusal("manifest", "hub-cache:team/app/sha256__"+hex+"/manifest.json", "reader"),
		},
		{
			"manifest by tag in a remote's cache", KindManifest, remote, "latest", http.MethodGet,
			user, pullRefusal("manifest", "hub-cache:team/app/latest/manifest.json", "reader"),
		},
		{
			"blob", KindBlob, local, "sha256:" + hex, http.MethodGet, user,
			pullRefusal("blob", "docker-local:team/app/sha256__"+hex, "reader"),
		},
		{
			"blob under another algorithm", KindBlob, local, "sha512:" + hex, http.MethodGet, user,
			pullRefusal("blob", "docker-local:team/app", "reader"),
		},
		{
			"tags list", KindTags, local, "", http.MethodGet, user,
			pullRefusal("tags", "docker-local:team/app", "reader"),
		},
		{
			"referrers", KindReferrers, local, "sha256:" + hex, http.MethodGet, user,
			pullRefusal("referrers", "docker-local:team/app", "reader"),
		},
		{
			"anonymous", KindManifest, local, "v1", http.MethodGet, kernel.Anonymous,
			pullRefusal("manifest", "docker-local:team/app/v1/manifest.json", "anonymous"),
		},
		{
			"no name", KindManifest, local, "v1", http.MethodGet,
			kernel.Principal{},
			pullRefusal("manifest", "docker-local:team/app/v1/manifest.json", "anonymous"),
		},
		{
			"transient token", KindManifest, local, "v1", http.MethodGet,
			kernel.Principal{Name: "ci", Identity: kernel.IdentityTransient},
			pullRefusal("manifest", "docker-local:team/app/v1/manifest.json", "token:ci"),
		},
		{
			"synthetic token", KindManifest, local, "v1", http.MethodGet,
			kernel.Principal{Name: "ci", Identity: kernel.IdentitySynthetic},
			pullRefusal("manifest", "docker-local:team/app/v1/manifest.json", "token:ci"),
		},
		{"manifest push", KindManifest, local, "v1", http.MethodPut, user, push},
		{"upload start", KindUploads, local, "", http.MethodPost, user, push},
		{"upload chunk", KindUpload, local, "id", http.MethodPatch, user, push},
		{"upload finish", KindUpload, local, "id", http.MethodPut, user, push},
		{"upload status", KindUpload, local, "id", http.MethodGet, user, push},
		{"upload cancel", KindUpload, local, "id", http.MethodDelete, user, push},
		{
			"manifest delete", KindManifest, local, "v1", http.MethodDelete, user,
			"The client does not have permission to delete from the repository.",
		},
		{
			"blob delete", KindBlob, local, "sha256:" + hex, http.MethodDelete, user,
			"The client does not have permission to delete from the repository.",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := Request{Kind: c.kind, Repo: c.repo, Name: "team/app", Reference: c.reference}
			rec := httptest.NewRecorder()
			Write(rec, q.refuse(c.method, c.p))
			if rec.Code != http.StatusForbidden || rec.Body.String() != refusalBody(c.message) ||
				rec.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("%d %v %s, want 403 %s", rec.Code, rec.Header(), rec.Body, c.message)
			}
		})
	}
}

// TestAccRequireAsksPermissionsFirst: a registry token holder whose
// permissions refuse the image gets 7.x's 403, not the token's 401, which
// the token endpoint's dropped scope would otherwise give a docker pull by
// a user without Read; a holder the permissions allow whose token lacks
// the action still gets the insufficient scope's 401. A server that
// authenticates every request refuses a request without a decider: 401
// with no principal, 7.x's 403 with one.
func TestAccRequireAsksPermissionsFirst(t *testing.T) {
	ctx := context.Background()
	db := fixture(t)
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "writers",
		Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{
				"writer": accessapi.ActionRead | accessapi.ActionWrite,
			}},
			Targets: map[string]accesstest.Target{"docker-local": {}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	c, err := db.Repos().Get(ctx, "docker-local")
	if err != nil {
		t.Fatal(err)
	}
	q := Request{
		Kind: KindManifest, RepoKey: "docker-local", Repo: c, Name: "priv", Reference: "v1",
		base: "/v2/docker-local",
	}
	kit := accesstest.Over(t, db)
	none := &kernel.RegistryAccess{}
	pull := &kernel.RegistryAccess{Grants: []kernel.RegistryGrant{
		{Type: scopeRepository, Name: "docker-local/priv", Actions: []string{actionPull}},
	}}
	refused := pullRefusal("manifest", "docker-local:priv/v1/manifest.json", "reader")
	for _, tc := range []struct {
		name    string
		p       kernel.Principal
		status  int
		message string
	}{
		{
			"reader's token, image refused",
			kernel.Principal{Name: "reader", Registry: none},
			http.StatusForbidden, refused,
		},
		{
			"anonymous token, image refused",
			kernel.Principal{Name: kernel.AnonymousUser, Anonymous: true, Registry: none},
			http.StatusForbidden,
			pullRefusal("manifest", "docker-local:priv/v1/manifest.json", "anonymous"),
		},
		{
			"writer's token without pull",
			kernel.Principal{Name: "writer", Registry: none},
			http.StatusUnauthorized, "the token does not grant this access to docker-local/priv",
		},
		{"writer's token with pull", kernel.Principal{Name: "writer", Registry: pull}, 0, ""},
	} {
		authorized, err := kit.Authorize(ctx, tc.p, tc.p.Anonymous)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(authorized)
		err = q.Require(r, "priv/v1/manifest.json", accessapi.ActionRead)
		if tc.status == 0 {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		rec := httptest.NewRecorder()
		Write(rec, err)
		if rec.Code != tc.status || rec.Body.String() != refusalBody(tc.message) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	authenticated := q
	authenticated.authenticated = true
	reader := kernel.Principal{Name: "reader"}
	for name, tc := range map[string]struct {
		p       *kernel.Principal
		status  int
		message string
	}{
		"no principal":             {nil, http.StatusUnauthorized, "authentication required"},
		"stored without a decider": {&reader, http.StatusForbidden, refused},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.p != nil {
			r = r.WithContext(kernel.WithPrincipal(r.Context(), *tc.p))
		}
		rec := httptest.NewRecorder()
		Write(rec, authenticated.Require(r, "priv/v1/manifest.json", accessapi.ActionRead))
		if rec.Code != tc.status || rec.Body.String() != refusalBody(tc.message) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
