package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/idtoken/idtokentest"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// oidcWorld is a server with a fake OIDC provider registered as ci, and two
// repositories: team-local, which the ci-team group may read, and
// open-local, which everyone (the auto-join group) may read.
type oidcWorld struct {
	h      http.Handler
	issuer *idtokentest.Issuer
}

func newOIDCWorld(t *testing.T) oidcWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Groups().Create(ctx, types.Group{Name: "ci-team"}))
	must(db.Groups().Create(ctx, types.Group{Name: "everyone", AutoJoin: true}))
	blobs, err := fs.New(t.TempDir())
	must(err)
	items := &item.Service{Store: db, Blobs: blobs}
	for key, group := range map[string]string{"team-local": "ci-team", "open-local": "everyone"} {
		c := repo.Defaults(&repo.Config{Key: key, Class: repo.Local})
		must(db.Repos().Create(ctx, *c))
		must(db.Permissions().Create(ctx, types.Permission{
			Name: "read-" + key,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Groups: map[string]types.Actions{group: types.ActionRead}},
				Targets: map[string]types.Target{key: {Include: []string{"**"}}},
			}},
		}))
		_, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: *c, Path: "f.txt"}, Body: strings.NewReader(key),
		})
		must(err)
	}
	must(db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}))
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	must(err)
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens, Groups: db.Groups(),
	})
	must(err)
	issuer, err := idtokentest.New()
	must(err)
	t.Cleanup(issuer.Close)
	w := oidcWorld{
		h: New(Config{
			CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, Tokens: tokens,
			PasswordHash: testHash,
		}),
		issuer: issuer,
	}
	admin := jsonAs(basic("admin", "pw"))
	w.call(t, http.MethodPost, "/access/api/v1/oidc", admin, `{"name":"ci","issuer_url":"`+
		issuer.URL()+`","provider_type":"generic","audience":"jfrog"}`, http.StatusOK)
	for _, m := range []string{
		// Tried first: any repository of octo-org gets the ci-team group,
		// through a synthetic subject.
		`{"name":"readers","priority":1,"claims":{"repository":"octo-org/*"},` +
			`"token_spec":{"scope":"applied-permissions/groups:ci-team","expires_in":60}}`,
		// A named user who is not stored: a transient user.
		`{"name":"deployer","priority":2,"claims":{"ref":"refs/heads/main"},` +
			`"token_spec":{"username":"ci-bot","scope":"applied-permissions/user","expires_in":120}}`,
	} {
		w.call(
			t,
			http.MethodPost,
			"/access/api/v1/oidc/ci/identity_mappings",
			admin,
			m,
			http.StatusOK,
		)
	}
	return w
}

func (w oidcWorld) call(
	t *testing.T,
	method, path string,
	headers map[string]string,
	body string,
	want int,
) string {
	t.Helper()
	rec := do(t, w.h, method, path, headers, body)
	if rec.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	return rec.Body.String()
}

func (w oidcWorld) mint(t *testing.T, aud string, ttl time.Duration, claims map[string]any) string {
	t.Helper()
	raw, err := w.issuer.Mint(aud, ttl, claims)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func exchangeBody(subject string, extra string) string {
	return `{"grant_type":"urn:ietf:params:oauth:grant-type:token-exchange",` +
		`"subject_token_type":"urn:ietf:params:oauth:token-type:id_token",` +
		`"subject_token":"` + subject + `","provider_name":"ci","provider_type":"GenericOidc"` +
		extra + `}`
}

type exchangeResult struct {
	AccessToken     string `json:"access_token"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int    `json:"expires_in"`
	IssuedTokenType string `json:"issued_token_type"`
	Username        string `json:"username"`
	Scope           string `json:"scope"`
	ReferenceToken  string `json:"reference_token"`
}

func (w oidcWorld) exchange(t *testing.T, body string, want int) exchangeResult {
	t.Helper()
	out := w.call(t, http.MethodPost, "/access/api/v1/oidc/token",
		map[string]string{"Content-Type": "application/json"}, body, want)
	var r exchangeResult
	if want == http.StatusOK {
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// TestAccOIDCExchange exchanges a CI provider's ID tokens and uses what comes
// back, as jf eot and setup-jfrog-cli do.
func TestAccOIDCExchange(t *testing.T) {
	w := newOIDCWorld(t)
	push := map[string]any{
		"repository": "octo-org/app",
		"ref":        "refs/heads/main",
		"sub":        "repo:octo-org/app",
	}

	// The highest-priority mapping wins: a synthetic subject with the
	// ci-team group alone, even though the deployer mapping also matches.
	got := w.exchange(t, exchangeBody(w.mint(t, "jfrog", time.Hour, push), ""), http.StatusOK)
	if got.Username != "oidc-ci-readers" || got.TokenType != "Bearer" || got.ExpiresIn != 60 ||
		got.IssuedTokenType != "urn:ietf:params:oauth:token-type:access_token" ||
		got.Scope != "applied-permissions/groups:ci-team" || got.ReferenceToken != "" {
		t.Fatalf("exchange = %+v", got)
	}
	bearer := bearerAuth(got.AccessToken)
	w.call(t, http.MethodGet, "/artifactory/team-local/f.txt", bearer, "", http.StatusOK)
	// The synthetic subject joins no auto-join group.
	w.call(t, http.MethodGet, "/artifactory/open-local/f.txt", bearer, "", http.StatusForbidden)

	// Naming the mapping picks it: a transient user, who joins the groups
	// new users join, and a reference token on request.
	got = w.exchange(t, exchangeBody(w.mint(t, "jfrog", time.Hour, push),
		`,"identity_mapping_name":"deployer","include_reference_token":true,"repo":"octo-org/app"`),
		http.StatusOK)
	if got.Username != "ci-bot" || got.ExpiresIn != 120 || got.ReferenceToken == "" {
		t.Fatalf("named mapping = %+v", got)
	}
	w.call(t, http.MethodGet, "/artifactory/open-local/f.txt", bearerAuth(got.ReferenceToken), "",
		http.StatusOK)
	// setup-jfrog-cli may use the username with the token as a password.
	w.call(t, http.MethodGet, "/artifactory/open-local/f.txt",
		basicToken("ci-bot", got.AccessToken), "", http.StatusOK)

	// The deployer mapping alone matches a tag build from elsewhere.
	tag := map[string]any{"repository": "other-org/app", "ref": "refs/heads/main"}
	if got := w.exchange(
		t,
		exchangeBody(w.mint(t, "jfrog", time.Hour, tag), ""),
		http.StatusOK,
	); got.Username != "ci-bot" {
		t.Fatalf("second mapping = %+v", got)
	}

	for name, c := range map[string]struct {
		body     string
		expected int
	}{
		"no mapping matches": {exchangeBody(w.mint(t, "jfrog", time.Hour, map[string]any{"repository": "x/y"}), ""), 400},
		"unknown mapping":    {exchangeBody(w.mint(t, "jfrog", time.Hour, push), `,"identity_mapping_name":"nope"`), 400},
		"other audience":     {exchangeBody(w.mint(t, "someone-else", time.Hour, push), ""), 401},
		"expired":            {exchangeBody(w.mint(t, "jfrog", -5*time.Minute, push), ""), 401},
		"wrong grant":        {strings.Replace(exchangeBody("x", ""), "token-exchange", "jwt-bearer", 1), 400},
		"wrong type":         {strings.Replace(exchangeBody("x", ""), "id_token", "jwt", 1), 400},
		"no subject token":   {exchangeBody("", ""), 400},
	} {
		rec := do(t, w.h, http.MethodPost, "/access/api/v1/oidc/token",
			map[string]string{"Content-Type": "application/json"}, c.body)
		if rec.Code != c.expected {
			t.Errorf("%s: status %d, want %d: %s", name, rec.Code, c.expected, rec.Body)
		}
	}

	// A provider whose keys cannot be fetched answers 401.
	w.issuer.FailDiscovery.Store(true)
	fresh := newOIDCWorld(t)
	fresh.issuer.FailDiscovery.Store(true)
	fresh.exchange(
		t,
		exchangeBody(fresh.mint(t, "jfrog", time.Hour, push), ""),
		http.StatusUnauthorized,
	)
}

func basicToken(user, tok string) map[string]string { return basic(user, tok) }

// TestAccOIDCSelfMint: a transient user from an exchange mints a token for
// itself, as jf atc does with the exchanged token configured. The token
// carries the exchanged token's scope and transient identity, so it reads
// what the exchanged token reads and no more, and it expires with the
// exchanged token. A synthetic subject's groups-only token mints nothing
// (#456).
func TestAccOIDCSelfMint(t *testing.T) {
	w := newOIDCWorld(t)
	admin := jsonAs(basic("admin", "pw"))
	// ci-team may also deploy to team-local, which the auto-join groups
	// may not: only a token that keeps the groups scope deploys there.
	w.call(
		t,
		http.MethodPut,
		"/artifactory/api/v2/security/permissions/deploy-team",
		admin,
		`{"repo":{"repositories":["team-local"],"actions":{"groups":{"ci-team":["read","write"]}}}}`,
		http.StatusOK,
	)
	w.call(t, http.MethodPost, "/access/api/v1/oidc/ci/identity_mappings", admin,
		`{"name":"both","priority":3,"claims":{"ref":"refs/tags/*"},`+
			`"token_spec":{"username":"ci-both","scope":"applied-permissions/groups:ci-team",`+
			`"expires_in":300}}`, http.StatusOK)
	for name, c := range map[string]struct {
		claims  map[string]any
		mapping string
		deploy  int
	}{
		"user scope":      {map[string]any{"ref": "refs/heads/main"}, "deployer", http.StatusForbidden},
		"user and groups": {map[string]any{"ref": "refs/tags/v1"}, "both", http.StatusCreated},
	} {
		got := w.exchange(t, exchangeBody(w.mint(t, "jfrog", time.Hour, c.claims),
			`,"identity_mapping_name":"`+c.mapping+`"`), http.StatusOK)
		body := w.call(t, http.MethodPost, "/access/api/v1/tokens",
			jsonAs(bearerAuth(got.AccessToken)), `{"expires_in":86400}`, http.StatusOK)
		var mine exchangeResult
		if err := json.Unmarshal([]byte(body), &mine); err != nil {
			t.Fatal(err)
		}
		if mine.Scope != got.Scope || mine.ExpiresIn < 1 || mine.ExpiresIn > got.ExpiresIn {
			t.Fatalf("%s: self-minted %+v from %+v", name, mine, got)
		}
		for who, tok := range map[string]string{"exchanged": got.AccessToken, "own": mine.AccessToken} {
			w.call(t, http.MethodGet, "/artifactory/open-local/f.txt", bearerAuth(tok), "",
				http.StatusOK)
			// A new path each: replacing a file would also need delete.
			w.call(t, http.MethodPut, "/artifactory/team-local/"+c.mapping+"-"+who+".txt",
				bearerAuth(tok), "x", c.deploy)
		}
		w.call(t, http.MethodPost, "/access/api/v1/tokens", jsonAs(bearerAuth(got.AccessToken)),
			`{"refreshable":true}`, http.StatusForbidden)
	}
	synthetic := w.exchange(t, exchangeBody(w.mint(t, "jfrog", time.Hour,
		map[string]any{"repository": "octo-org/app"}), ""), http.StatusOK)
	w.call(t, http.MethodPost, "/access/api/v1/tokens", jsonAs(bearerAuth(synthetic.AccessToken)),
		`{}`, http.StatusForbidden)
}

// TestAccOIDCDynamicMappings follows exchanged tokens to what they may read: a
// resolved user adds the auto-join groups to a groups scope (IDZ-59), and a
// resource pattern narrows what the user and groups may read but never
// reaches beyond it (IDZ-63).
func TestAccOIDCDynamicMappings(t *testing.T) {
	w := newOIDCWorld(t)
	admin := jsonAs(basic("admin", "pw"))
	for _, m := range []string{
		`{"name":"named","claims":{"sub":"*"},"token_spec":{"username_pattern":"jf{{actor}}jf|$1",` +
			`"scope":"applied-permissions/groups:ci-team"}}`,
		`{"name":"narrowed","claims":{"sub":"*"},"token_spec":{"username_pattern":"{{actor}}",` +
			`"scope":"applied-permissions/groups:ci-team","resource_pattern":"{{jfrog_scope}}"}}`,
		`{"name":"groups-narrowed","claims":{"sub":"*"},"token_spec":` +
			`{"scope":"applied-permissions/groups:ci-team","resource_pattern":"{{jfrog_scope}}"}}`,
	} {
		w.call(t, http.MethodPost, "/access/api/v1/oidc/ci/identity_mappings", admin, m,
			http.StatusOK)
	}
	exchange := func(mapping string, claims map[string]any, want int) exchangeResult {
		t.Helper()
		claims["sub"] = "x"
		return w.exchange(t, exchangeBody(w.mint(t, "jfrog", time.Hour, claims),
			`,"identity_mapping_name":"`+mapping+`"`), want)
	}
	reads := func(got exchangeResult, team, open int) {
		t.Helper()
		bearer := bearerAuth(got.AccessToken)
		w.call(t, http.MethodGet, "/artifactory/team-local/f.txt", bearer, "", team)
		w.call(t, http.MethodGet, "/artifactory/open-local/f.txt", bearer, "", open)
	}

	got := exchange("named", map[string]any{"actor": "jfoctocatjf"}, http.StatusOK)
	if got.Username != "octocat" ||
		got.Scope != "applied-permissions/groups:ci-team applied-permissions/user" {
		t.Fatalf("named = %+v", got)
	}
	reads(got, http.StatusOK, http.StatusOK)
	// The claim must match the template.
	exchange("named", map[string]any{"actor": "octocat"}, http.StatusBadRequest)

	got = exchange("narrowed", map[string]any{"actor": "octocat", "jfrog_scope": "team-local"},
		http.StatusOK)
	if got.Scope != "applied-permissions/groups:ci-team artifact:team-local/**:r applied-permissions/user" {
		t.Fatalf("narrowed = %+v", got)
	}
	reads(got, http.StatusOK, http.StatusForbidden)
	got = exchange("narrowed", map[string]any{"actor": "octocat", "jfrog_scope": "open-local"},
		http.StatusOK)
	reads(got, http.StatusForbidden, http.StatusOK)
	// The synthetic subject's ci-team group cannot read open-local, and
	// naming it in the claim does not change that.
	got = exchange("groups-narrowed", map[string]any{"jfrog_scope": "open-local"}, http.StatusOK)
	reads(got, http.StatusForbidden, http.StatusForbidden)
	for _, scope := range []string{"artifact:*:r", "system:metrics:r", "team-local open-local"} {
		exchange("narrowed", map[string]any{"actor": "octocat", "jfrog_scope": scope},
			http.StatusBadRequest)
	}
}

// TestAccOIDCReadBack: providers and mappings read back exactly as the
// jfrog/platform Terraform provider wrote them, so a plan stays empty.
func TestAccOIDCReadBack(t *testing.T) {
	w := newOIDCWorld(t)
	admin := basic("admin", "pw")
	got := w.call(t, http.MethodGet, "/access/api/v1/oidc/ci", admin, "", http.StatusOK)
	var p map[string]any
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatal(err)
	}
	if p["provider_type"] != "Generic OpenID Connect" || p["audience"] != "jfrog" {
		t.Fatalf("provider = %s", got)
	}
	list := w.call(t, http.MethodGet, "/access/api/v1/oidc", admin, "", http.StatusOK)
	if !strings.Contains(list, `"name":"ci"`) {
		t.Fatalf("providers = %s", list)
	}
	mapping := `{"name":"proj","priority":3,"claims":{"aud":["a","b"],"n":1},"token_spec":` +
		`{"username":"dev","scope":"applied-permissions/roles:p1:\"Developer\"","audience":"*@*","expires_in":60}}`
	w.call(
		t,
		http.MethodPost,
		"/access/api/v1/oidc/ci/identity_mappings",
		jsonAs(admin),
		mapping,
		http.StatusOK,
	)
	dynamic := `{"name":"dynamic","token_spec":{"username":"dev","groups_pattern":"{{groups}} | $0-ci",` +
		`"resource_pattern":"{{jfrog_scope}}"}}`
	w.call(t, http.MethodPost, "/access/api/v1/oidc/ci/identity_mappings", jsonAs(admin), dynamic,
		http.StatusOK)
	back := w.call(t, http.MethodGet, "/access/api/v1/oidc/ci/identity_mappings/dynamic", admin, "",
		http.StatusOK)
	if !strings.Contains(back, `"resource_pattern":"{{jfrog_scope}}"`) ||
		!strings.Contains(back, `"groups_pattern":"{{groups}} | $0-ci"`) {
		t.Fatalf("dynamic mapping = %s", back)
	}
	one := w.call(
		t,
		http.MethodGet,
		"/access/api/v1/oidc/ci/identity_mappings/proj",
		admin,
		"",
		http.StatusOK,
	)
	var m map[string]any
	if err := json.Unmarshal([]byte(one), &m); err != nil {
		t.Fatal(err)
	}
	spec, _ := m["token_spec"].(map[string]any)
	if m["project_key"] != "p1" || spec["scope"] != `applied-permissions/roles:p1:"Developer"` ||
		spec["expires_in"] != float64(60) {
		t.Fatalf("mapping = %s", one)
	}
	scoped := w.call(
		t,
		http.MethodGet,
		"/access/api/v1/oidc/ci/identity_mappings?project_key=p1",
		admin,
		"",
		http.StatusOK,
	)
	if !strings.Contains(scoped, `"proj"`) || strings.Contains(scoped, `"readers"`) {
		t.Fatalf("project mappings = %s", scoped)
	}
	empty := w.call(
		t,
		http.MethodGet,
		"/access/api/v1/oidc/ci/identity_mappings?project_key=none",
		admin,
		"",
		http.StatusOK,
	)
	if strings.TrimSpace(empty) != "[]" {
		t.Fatalf("no mappings = %s", empty)
	}
}

// TestAccOIDCWithoutTokens: the exchange answers 501 without a token service,
// as the token API does.
func TestAccOIDCWithoutTokens(t *testing.T) {
	db := inmem.New()
	h := New(Config{CompatVersion: "7.155.0", Store: db})
	rec := do(t, h, http.MethodPost, "/access/api/v1/oidc/token",
		map[string]string{"Content-Type": "application/json"}, `{}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}
