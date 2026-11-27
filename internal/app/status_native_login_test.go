package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// Invalid capabilities are independent of request credentials. Positive
// sign-in/consent/exchange and current-authority cases use real SQL stores in
// test/server/access/native_login_test.go; these rows inventory every route.
var nativeLoginCases = []statusCase{
	{"POST /artifactory/api/npm/{repoKey}/-/v1/login", "/artifactory/api/npm/native-status/-/v1/login", "", `{}`, 200},
	{"POST /artifactory/api/npm/{repoKey}/-/v1/login", "/artifactory/api/npm/native-status/-/v1/login", "forged", `{}`, 200},
	{"GET /artifactory/api/npm/{repoKey}/-/v1/done", "/artifactory/api/npm/native-status/-/v1/done?sessionId=unknown", "", "", 400},
	{"GET /ui/native-login", "/ui/native-login?request=unknown", "", "", 400},
	{"POST /ui/native-login", "/ui/native-login?request=unknown", "", "action=approve", 400},
	{"GET /ui/terraform/oauth2/authorize", "/ui/terraform/oauth2/authorize", "", "", 400},
	{"POST /artifactory/api/oauth2/token", "/artifactory/api/oauth2/token", "", "grant_type=authorization_code", 400},
}

func nativeStatusTune(c *RouterConfig) {
	c.NativeLoginEnabled = true
	c.NativeLoginOrigin = "https://login.example"
}

func seedNativeStatus(t *testing.T, db *inmem.Store, _ blob.Store) {
	t.Helper()
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: "native-status", Class: repo.Local, PackageType: repo.NPM})); err != nil {
		t.Fatal(err)
	}
}

var nativeLoginStatusPart = statusPart{
	handwritten: func() map[string]bool {
		out := map[string]bool{}
		for _, c := range nativeLoginCases {
			out[c.route] = true
		}
		return out
	}(),
	rows: func() []statusCase { return nativeLoginCases }, seed: seedNativeStatus, tune: nativeStatusTune,
}

var nativeLoginMatrixPart = matrixPart{
	rows: func() []matrixRow {
		var out []matrixRow
		for _, c := range nativeLoginCases {
			if c.as != "" {
				continue
			}
			out = append(out, matrixRow{route: c.route, path: c.path, body: c.body, who: noCredentials, ok: c.expected, open: true})
		}
		return out
	},
	seed: func(t *testing.T, f *matrixFixture) { seedNativeStatus(t, f.db, nil) }, tune: nativeStatusTune,
}

// Route walks never call this embedded service. Keeping the enabled variant in
// inventory prevents default-off routes silently disappearing from HEAD/ledger
// qualification. Runtime status/authority tests bind the real access module.
func nativeRouteInventoryConfig() RouterConfig {
	return RouterConfig{
		Access: accessapi.API{NativeLogin: struct{ accessapi.NativeLogin }{}, Settings: accessapi.Settings{NativeLoginEnabled: true, NativeLoginOrigin: "https://login.example"}},
	}
}

func TestUnitNativeLoginDiscoveryRequiresService(t *testing.T) {
	for _, available := range []bool{false, true} {
		cfg := nativeRouteInventoryConfig()
		if !available {
			cfg.Access.NativeLogin = nil
		}
		h := NewRouter(cfg)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/terraform.json", nil))
		var doc map[string]json.RawMessage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &doc) != nil {
			t.Fatal(w.Code, w.Body)
		}
		if (doc["login.v1"] != nil) != available {
			t.Fatal("advertised unserved native approval", available, w.Body)
		}
		if doc["tfe.v2"] == nil || doc["modules.v1"] == nil || doc["providers.v1"] == nil {
			t.Fatal("lost existing discovery entries")
		}
	}
}

func TestAccNativeLoginRouteStatusAndPrincipals(t *testing.T) {
	h := statusFixture(t, false)
	for _, c := range nativeLoginCases {
		t.Run(c.route+"/"+c.as, func(t *testing.T) {
			var headers map[string]string
			if c.as == "forged" {
				headers = map[string]string{"Authorization": "Bearer forged"}
			}
			w := do(t, h, c.method(), c.path, headers, c.body)
			if w.Code != c.expected {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
	for _, r := range nativeLoginMatrixPart.rows() {
		t.Run(r.route+"/principals", func(t *testing.T) {
			f := newMatrixFixture(t)
			for _, p := range matrixPrincipals {
				if got, body := f.call(t, r, p); got != r.want(p) {
					t.Fatal(p.name, got, r.want(p), body)
				}
			}
		})
	}
}
