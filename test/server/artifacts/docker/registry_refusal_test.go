package docker_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// refusalWorld is registryWorld with docker-local beside the remote, which
// reader reads and nobody may deploy to, and anonymous access on: denied
// holds nothing, and anonymous nothing either.
func refusalWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := registryWorld(t)
	ctx := t.Context()
	local := repo.Defaults(
		&repo.Config{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
	)
	if err := w.rt.Store.Repos().Create(ctx, *local); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Permissions().Create(ctx, accesstest.Permission{
		Name: "local-read", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{"docker-local": {}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	return w
}

// tokenFor is the registry token user (anonymous when empty) obtains
// for scope, as a Bearer credential.
func tokenFor(t *testing.T, h http.Handler, user, scope string) map[string]string {
	t.Helper()
	var credentials map[string]string
	if user != "" {
		credentials = basic(user, "pw")
	}
	rec := do(t, h, http.MethodGet, "/v2/token?scope="+scope, credentials, "")
	var body struct{ Token string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		body.Token == "" {
		t.Fatalf("token for %q: %d %s", user, rec.Code, rec.Body)
	}
	return bearerAuth(body.Token)
}

// TestAccRegistryPermissionRefusals: a registry request the caller's
// permissions refuse answers 403 with the code UNAUTHORIZED and 7.x's
// message, whether the caller sent a password or a registry token, and an
// anonymous caller while anonymous access is on the same, with no
// challenge (#1276, RTFACT-27228, RTFACT-30133, RTFACT-22867). A pull
// names what it reads, the repository (a remote's cache) and the user; a
// push and a delete do not [I]. Kept: a request without credentials is
// challenged, and a token that lacks an action its user holds gets 401
// with the challenge for the scope it needs.
func TestAccRegistryPermissionRefusals(t *testing.T) {
	w := refusalWorld(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	pull := func(what, repoPath, user string) string {
		return `{"errors":[{"code":"UNAUTHORIZED","message":"The client does not have permission for ` +
			what + `: Download request for repo:path '` + repoPath + `' is forbidden for user: '` +
			user + `'."}]}` + "\n"
	}
	push := `{"errors":[{"code":"UNAUTHORIZED","message":"The client does not have permission ` +
		`to push to the repository."}]}` + "\n"
	manifest := `{"schemaVersion":2}`
	for _, c := range []struct {
		name, method, path string
		as                 map[string]string
		body               string
		want               string
	}{
		{
			"pull without Read", http.MethodGet, "/v2/docker-local/app/manifests/v1",
			basic("denied", "pw"), "", pull("manifest", "docker-local:app/v1/manifest.json", "denied"),
		},
		{
			"pull by digest without Read, under the prefix", http.MethodGet,
			"/artifactory/api/docker/docker-local/v2/app/manifests/" + digest,
			basic("denied", "pw"), "",
			pull("manifest", "docker-local:app/"+digest+"/manifest.json", "denied"),
		},
		{
			"pull with a token, without Read", http.MethodGet, "/v2/docker-local/app/manifests/v1",
			tokenFor(t, w.h, "denied", "repository:docker-local/app:pull"), "",
			pull("manifest", "docker-local:app/v1/manifest.json", "denied"),
		},
		{
			"blob read without Read", http.MethodGet, "/v2/docker-local/app/blobs/" + digest,
			basic("denied", "pw"), "",
			pull("blob", "docker-local:app/sha256__"+strings.Repeat("a", 64), "denied"),
		},
		{
			"anonymous blob read", http.MethodGet, "/v2/docker-local/app/blobs/" + digest,
			tokenFor(t, w.h, "", "repository:docker-local/app:pull"), "",
			pull("blob", "docker-local:app/sha256__"+strings.Repeat("a", 64), "anonymous"),
		},
		{
			"anonymous pull", http.MethodGet, "/v2/docker-local/app/manifests/v1",
			tokenFor(t, w.h, "", "repository:docker-local/app:pull"), "",
			pull("manifest", "docker-local:app/v1/manifest.json", "anonymous"),
		},
		{
			"tags list without Read", http.MethodGet,
			"/artifactory/api/docker/docker-local/v2/app/tags/list", basic("denied", "pw"), "",
			pull("tags", "docker-local:app", "denied"),
		},
		{
			"push without Deploy", http.MethodPost, "/v2/docker-local/app/blobs/uploads/",
			basic("reader", "pw"), "", push,
		},
		{
			"push with a token, without Deploy", http.MethodPut, "/v2/docker-local/app/manifests/v1",
			withHeaders(
				tokenFor(t, w.h, "reader", "repository:docker-local/app:pull,push"),
				map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"},
			), manifest, push,
		},
		{
			"anonymous push", http.MethodPost, "/v2/docker-local/app/blobs/uploads/",
			tokenFor(t, w.h, "", "repository:docker-local/app:pull,push"), "", push,
		},
		{
			"delete without Delete", http.MethodDelete, "/v2/docker-local/app/manifests/v1",
			basic("reader", "pw"), "",
			`{"errors":[{"code":"UNAUTHORIZED","message":"The client does not have permission ` +
				`to delete from the repository."}]}` + "\n",
		},
		{
			"remote pull without Read", http.MethodGet, "/v2/remote/app/manifests/" + digest,
			basic("denied", "pw"), "",
			pull("manifest", "remote-cache:app/sha256__"+strings.Repeat("a", 64)+"/manifest.json",
				"denied"),
		},
		{
			"anonymous remote pull", http.MethodGet, "/v2/remote/app/manifests/v1",
			tokenFor(t, w.h, "", "repository:remote/app:pull"), "",
			pull("manifest", "remote-cache:app/v1/manifest.json", "anonymous"),
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(w.up.Requests())
			rec := do(t, w.h, c.method, c.path, c.as, c.body)
			if rec.Code != http.StatusForbidden || rec.Body.String() != c.want ||
				rec.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("%d %v %s, want 403 %s", rec.Code, rec.Header(), rec.Body, c.want)
			}
			if n := len(w.up.Requests()) - before; n != 0 {
				t.Fatalf("%d upstream requests for a refused caller", n)
			}
		})
	}
	for name, c := range map[string]struct {
		as        map[string]string
		challenge string
	}{
		"no credentials": {
			nil, `Bearer realm="http://example.com/artifactory/api/docker/docker-local/v2/token",` +
				`service="example.com",scope="repository:docker-local/app:pull"`,
		},
		"a token for another image": {
			tokenFor(t, w.h, "reader", "repository:docker-local/other:pull"),
			`Bearer realm="http://example.com/artifactory/api/docker/docker-local/v2/token",` +
				`service="example.com",scope="repository:docker-local/app:pull"`,
		},
	} {
		rec := do(t, w.h, http.MethodGet, "/v2/docker-local/app/manifests/v1", c.as, "")
		if rec.Code != http.StatusUnauthorized ||
			rec.Header().Get("WWW-Authenticate") != c.challenge {
			t.Errorf("%s: %d %v %s", name, rec.Code, rec.Header(), rec.Body)
		}
	}
}

// withHeaders is a with b's headers added.
func withHeaders(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range []map[string]string{a, b} {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// containerdRequest sends method path as containerd's resolver does
// (core/remotes/docker: retryRequest in resolver.go, AddResponses and
// invalidAuthorization in authorizer.go) and returns the last answer and
// the attempts it made. A 401 with a Bearer challenge is sent again, five
// attempts in all; a challenge with an error parameter fetches a token
// once, and a second 401 to the same request ends it.
func containerdRequest(
	t *testing.T, h http.Handler, method, path string, as map[string]string,
) (last *httptest.ResponseRecorder, attempts int) {
	t.Helper()
	for attempts < 5 {
		last = do(t, h, method, path, as, "")
		attempts++
		challenge := last.Header().Get("WWW-Authenticate")
		if last.Code != http.StatusUnauthorized || !strings.HasPrefix(challenge, "Bearer ") ||
			strings.Contains(challenge, "error=") && attempts >= 2 {
			break
		}
	}
	return last, attempts
}

// TestAccRegistryRemoteUpstreamRefusal: when a remote's upstream refuses
// the remote's own credentials, the 401 relayed under the registry's paths
// keeps the registry's challenge, which a 401 must carry (RFC 9110,
// 15.5.2), with error="invalid_request" [I], so that containerd gives up
// on its second attempt instead of its fifth: one client request reaches
// the upstream twice, not five times (#1277). The body is 7.x's message
// naming the remote (RTFACT-30467), for a manifest and a blob, GET and
// HEAD, on the repository path and under the Artifactory prefix.
func TestAccRegistryRemoteUpstreamRefusal(t *testing.T) {
	w := registryWorld(t)
	digest := "sha256:" + strings.Repeat("b", 64)
	for _, p := range []string{"/v2/team/image/manifests/latest", "/v2/team/image/blobs/" + digest} {
		w.up.Set(p, fakeupstream.Resource{
			Status: http.StatusUnauthorized,
			Header: http.Header{"Www-Authenticate": {`Basic realm="upstream"`}},
			Body:   []byte(`{"errors":[{"code":"UNAUTHORIZED","message":"upstream"}]}`),
		})
	}
	const (
		realm = `Bearer realm="http://example.com/artifactory/api/docker/remote/v2/token",` +
			`service="example.com",`
		body = `{"errors":[{"message":"Invalid username/password configured for Remote ` +
			`Docker repository: remote","status":401}]}` + "\n"
	)
	token := tokenFor(t, w.h, "reader", "repository:remote/team/image:pull")
	prefixToken := do(t, w.h, http.MethodGet,
		"/artifactory/api/docker/remote/v2/token?scope=repository:team/image:pull",
		basic("reader", "pw"), "")
	var prefixed struct{ Token string }
	if prefixToken.Code != http.StatusOK ||
		json.Unmarshal(prefixToken.Body.Bytes(), &prefixed) != nil {
		t.Fatalf("token under the prefix: %d %s", prefixToken.Code, prefixToken.Body)
	}
	for _, c := range []struct {
		path, scope string
		as          map[string]string
	}{
		{"/v2/remote/team/image/manifests/latest", "repository:remote/team/image:pull", token},
		{"/v2/remote/team/image/blobs/" + digest, "repository:remote/team/image:pull", token},
		{
			"/artifactory/api/docker/remote/v2/team/image/manifests/latest",
			"repository:team/image:pull", bearerAuth(prefixed.Token),
		},
		{
			"/artifactory/api/docker/remote/v2/team/image/blobs/" + digest,
			"repository:team/image:pull", bearerAuth(prefixed.Token),
		},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			before := len(w.up.Requests())
			rec, attempts := containerdRequest(t, w.h, method, c.path, c.as)
			want := realm + `scope="` + c.scope + `",error="invalid_request"`
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != want {
				t.Errorf(
					"%s %s: %d %q",
					method,
					c.path,
					rec.Code,
					rec.Header().Get("WWW-Authenticate"),
				)
			}
			if method == http.MethodGet && rec.Body.String() != body {
				t.Errorf("%s %s: %s", method, c.path, rec.Body)
			}
			if upstream := len(w.up.Requests()) - before; attempts != 2 || upstream != 2 {
				t.Errorf("%s %s: %d attempts, %d upstream requests, want 2 and 2",
					method, c.path, attempts, upstream)
			}
		}
	}
}
