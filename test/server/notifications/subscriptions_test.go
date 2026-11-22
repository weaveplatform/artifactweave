package notifications_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	subscriptions = "/event/api/v1/subscriptions"
	platformURL   = "https://platform.example"
	privateValue  = "web02-server-private-value"
	rotatedValue  = "web02-server-rotated-value"
	deniedValue   = "web02-server-denied-value"
)

type subscriptionWorld struct {
	t     *testing.T
	store app.RouterStore
	h     http.Handler
	logs  *bytes.Buffer
}

func newSubscriptionWorld(t *testing.T, st app.RouterStore) subscriptionWorld {
	t.Helper()
	ctx := t.Context()
	support.Seed(t, st)
	if err := instance.EnsureInstance(ctx, st); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, st, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := authn.NewAuthenticator(st.Users(), st.Settings(), authn.Options{
		Hash: support.TestHash, Tokens: tokens, Groups: st.Groups(), APIRestrictions: st.APIRestrictions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project-admin", "reader", "manager"} {
		if err := st.Users().Create(ctx, accesstest.User{Name: name, PasswordHash: support.CheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	logs := new(bytes.Buffer)
	w := subscriptionWorld{t: t, store: st, logs: logs, h: harness.Router(harness.Config{
		Store: st, Auth: auth, Tokens: tokens, AllowBasicAuthTokens: true, BaseURL: platformURL,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})}
	for _, key := range []string{"alpha", "beta"} {
		w.request(http.MethodPost, "/access/api/v1/projects", "admin", `{"project_key":"`+key+`","display_name":"`+key+`"}`, http.StatusCreated)
	}
	w.request(http.MethodPut, "/access/api/v1/projects/alpha/users/project-admin", "admin", `{"roles":["Project Admin"]}`, http.StatusOK)
	return w
}

func (w subscriptionWorld) request(method, path, actor, body string, want int) *httptest.ResponseRecorder {
	w.t.Helper()
	headers := map[string]string{"Content-Type": "application/json"}
	if strings.HasPrefix(actor, "Bearer ") {
		headers["Authorization"] = actor
	} else if actor != "" {
		for k, v := range support.Basic(actor, "pw") {
			headers[k] = v
		}
	}
	r := support.Do(w.t, w.h, method, path, headers, body)
	for _, secret := range []string{privateValue, rotatedValue, deniedValue} {
		if strings.Contains(r.Body.String(), secret) || strings.Contains(w.logs.String(), secret) {
			w.t.Fatal("subscription response, error or log disclosed a fixture secret")
		}
	}
	if r.Code != want {
		w.t.Fatalf("%s %s as %s: status=%d want=%d body=%s", method, path, strings.Split(actor, " ")[0], r.Code, want, r.Body.String())
	}
	return r
}

func subscriptionBody(key, project, domain, event, receiver string) string {
	projectField := ""
	if project != "" {
		projectField = `,"project_key":"` + project + `"`
	}
	return `{"key":"` + key + `","enabled":true` + projectField + `,"event_filter":{"domain":"` + domain + `","event_types":["` + event + `"],"criteria":{}},"handlers":[{"handler_type":"webhook","url":"` + receiver + `","secret":"` + privateValue + `"}]}`
}

func subscriptionRouteContract(t *testing.T, store app.RouterStore) {
	t.Helper()
	w := newSubscriptionWorld(t, store)
	var delivered atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { delivered.Add(1); w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(receiver.Close)
	body := subscriptionBody("plain", "alpha", "artifact", "deployed", receiver.URL)
	w.request(http.MethodPost, subscriptions, "admin", body, http.StatusCreated)
	w.request(http.MethodPost, subscriptions, "admin", body, http.StatusBadRequest)
	w.request(http.MethodGet, subscriptions+"/plain", "admin", "", http.StatusOK)
	update := strings.ReplaceAll(body, `,"secret":"`+privateValue+`"`, "")
	w.request(http.MethodPut, subscriptions+"/plain", "admin", update, http.StatusNoContent)
	stored, err := store.Webhooks().Get(t.Context(), "plain")
	if err != nil || stored.Handlers[0].Secret != privateValue {
		t.Fatal("omitted update did not preserve the secret", err)
	}
	w.request(http.MethodPut, subscriptions+"/plain", "admin", strings.ReplaceAll(update, `"project_key":"alpha"`, `"project_key":"beta"`), http.StatusBadRequest)
	w.request(
		http.MethodPut,
		subscriptions+"/plain",
		"admin",
		strings.ReplaceAll(update, `"url":"`+receiver.URL+`"`, `"url":"not-an-absolute-url","secret":"`+rotatedValue+`"`),
		http.StatusBadRequest,
	)
	stored, err = store.Webhooks().Get(t.Context(), "plain")
	if err != nil || stored.ProjectKey != "alpha" || stored.Handlers[0].Secret != privateValue {
		t.Fatal("malformed replacement changed immutable ownership or secret", err)
	}
	custom := `{"key":"custom","enabled":true,"project_key":"alpha","event_filter":{"domain":"release_bundle_v2","event_types":["release_bundle_v2_completed"],"criteria":{"selectedReleaseBundles":{"bundle":[]}}},"handlers":[{"handler_type":"custom-webhook","url":"` + receiver.URL + `/{{.secrets.token}}","method":"PATCH","payload":"{{json .data}}","secrets":[{"name":"token","value":"` + privateValue + `"}],"http_headers":[{"name":"X-Token","value":"{{.secrets.token}}"}]}]}`
	w.request(http.MethodPost, subscriptions, "admin", custom, http.StatusCreated)
	// Both lifecycle resources in jfrog/artifactory 12.11.14 omit criteria on
	// create and update; other domains must retain their presence validation.
	for _, handler := range []string{"webhook", "custom-webhook"} {
		lifecycle := subscriptionBody("lifecycle-"+handler, "", "artifact_lifecycle", "archive", receiver.URL)
		lifecycle = strings.ReplaceAll(lifecycle, `,"criteria":{}`, "")
		lifecycle = strings.ReplaceAll(lifecycle, `"handler_type":"webhook"`, `"handler_type":"`+handler+`"`)
		if handler == "custom-webhook" {
			lifecycle = strings.ReplaceAll(lifecycle, `,"secret":"`+privateValue+`"`, "")
		}
		w.request(http.MethodPost, subscriptions, "admin", lifecycle, http.StatusCreated)
		w.request(http.MethodPut, subscriptions+"/lifecycle-"+handler, "admin", lifecycle, http.StatusNoContent)
		w.request(http.MethodGet, subscriptions+"/lifecycle-"+handler, "admin", "", http.StatusOK)
	}
	r := w.request(http.MethodGet, subscriptions+"/custom", "admin", "", http.StatusOK)
	if !strings.Contains(r.Body.String(), `"http_headers"`) || strings.Contains(r.Body.String(), `"headers"`) || strings.Contains(r.Body.String(), `"type"`) {
		t.Fatal("custom handler storage fields leaked", r.Body.String())
	}
	w.request(http.MethodPost, subscriptions+"/test", "admin", body, http.StatusNotImplemented)
	w.request(http.MethodGet, "/event/api/v1/troubleshooting?subscription=plain", "admin", "", http.StatusNotImplemented)
	w.request(http.MethodDelete, subscriptions+"/plain", "admin", "", http.StatusNoContent)
	w.request(http.MethodGet, subscriptions+"/plain", "admin", "", http.StatusNotFound)
	if delivered.Load() != 0 {
		t.Fatal("subscription CRUD performed deferred delivery")
	}
}

func TestAccWebhookSubscriptionApplicationRoutes(t *testing.T) {
	subscriptionRouteContract(t, inmem.New())
}

func TestAccWebhookSubscriptionSQLApplicationRoutes(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(t, appsql.Modules(), func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			subscriptionRouteContract(t, newStore(t))
			subscriptionPrincipalMatrix(t, newStore(t))
		}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

func (w subscriptionWorld) scopedToken(raw string) string {
	w.t.Helper()
	body, err := json.Marshal(map[string]any{"scope": raw, "expires_in": 600})
	if err != nil {
		w.t.Fatal(err)
	}
	r := w.request(http.MethodPost, "/access/api/v1/tokens", "admin", string(body), http.StatusOK)
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &issued); err != nil || issued.AccessToken == "" {
		w.t.Fatal("token response missing access token", err)
	}
	return "Bearer " + issued.AccessToken
}

func TestAccWebhookSubscriptionCurrentPrincipalAndScopeMatrix(t *testing.T) {
	subscriptionPrincipalMatrix(t, inmem.New())
}

func subscriptionPrincipalMatrix(t *testing.T, store app.RouterStore) {
	t.Helper()
	w := newSubscriptionWorld(t, store)
	for _, body := range []string{
		subscriptionBody("alpha-artifact", "alpha", "artifact", "deployed", "https://receiver.example"),
		subscriptionBody("beta-artifact", "beta", "artifact", "deployed", "https://receiver.example"),
		subscriptionBody("alpha-future", "alpha", "release_bundle_v2", "release_bundle_v2_completed", "https://receiver.example"),
		subscriptionBody("global-user", "", "user", "locked", "https://receiver.example"),
	} {
		w.request(http.MethodPost, subscriptions, "admin", body, http.StatusCreated)
	}
	w.request(http.MethodGet, subscriptions, "", "", http.StatusUnauthorized)
	w.request(http.MethodGet, subscriptions, "reader", "", http.StatusForbidden)
	r := w.request(http.MethodGet, subscriptions, "project-admin", "", http.StatusOK)
	if strings.Contains(r.Body.String(), "beta-artifact") || strings.Contains(r.Body.String(), "alpha-future") || strings.Contains(r.Body.String(), "global-user") ||
		!strings.Contains(r.Body.String(), "alpha-artifact") {
		t.Fatal("project listing disclosed an unauthorized project/domain", r.Body.String())
	}
	w.request(http.MethodGet, subscriptions+"/beta-artifact", "project-admin", "", http.StatusForbidden)
	for _, domain := range []string{"artifact", "artifact_property", "docker", "build"} {
		event := map[string]string{"artifact": "deployed", "artifact_property": "added", "docker": "pushed", "build": "uploaded"}[domain]
		body := subscriptionBody("owned-"+domain, "alpha", domain, event, "https://receiver.example")
		w.request(http.MethodPost, subscriptions, "project-admin", body, http.StatusCreated)
		w.request(http.MethodPost, subscriptions, "project-admin", strings.ReplaceAll(body, "alpha", "beta"), http.StatusForbidden)
	}
	w.request(
		http.MethodPost,
		subscriptions,
		"project-admin",
		subscriptionBody("future-denied", "alpha", "release_bundle_v2", "release_bundle_v2_completed", "https://receiver.example"),
		http.StatusForbidden,
	)
	manager, err := w.store.Users().Get(t.Context(), "manager")
	if err != nil {
		t.Fatal(err)
	}
	manager.Roles.ManageWebhook = true
	if err := w.store.Users().Update(t.Context(), manager); err != nil {
		t.Fatal(err)
	}
	w.request(http.MethodGet, subscriptions+"/global-user", "manager", "", http.StatusOK)
	manager.Roles.ManageWebhook = false
	if err := w.store.Users().Update(t.Context(), manager); err != nil {
		t.Fatal(err)
	}
	w.request(http.MethodGet, subscriptions, "manager", "", http.StatusForbidden)
	read := w.scopedToken("system:webhooks@alpha/" + platformURL + ":r")
	w.request(http.MethodGet, subscriptions+"/alpha-future", read, "", http.StatusOK)
	w.request(http.MethodGet, subscriptions+"/beta-artifact", read, "", http.StatusForbidden)
	before, err := w.store.Webhooks().Get(t.Context(), "alpha-artifact")
	if err != nil {
		t.Fatal(err)
	}
	denied := strings.ReplaceAll(subscriptionBody("alpha-artifact", "alpha", "artifact", "deployed", "https://receiver.example"), privateValue, deniedValue)
	w.request(http.MethodPut, subscriptions+"/alpha-artifact", read, denied, http.StatusForbidden)
	w.request(http.MethodDelete, subscriptions+"/alpha-artifact", read, "", http.StatusForbidden)
	after, err := w.store.Webhooks().Get(t.Context(), "alpha-artifact")
	if err != nil || !reflect.DeepEqual(before, after) || after.Handlers[0].Secret != privateValue {
		t.Fatal("action-scoped denial changed the stored subscription or protected secret", err)
	}
	wrongURL := w.scopedToken("system:webhooks@alpha/https://receiver.example:r")
	w.request(http.MethodGet, subscriptions, wrongURL, "", http.StatusForbidden)
	resource := w.scopedToken("applied-permissions/user artifact:*:r")
	w.request(http.MethodGet, subscriptions, resource, "", http.StatusForbidden)
	w.request(http.MethodDelete, "/access/api/v1/projects/alpha/users/project-admin", "admin", "", http.StatusNoContent)
	w.request(http.MethodGet, subscriptions, "project-admin", "", http.StatusForbidden)
}
