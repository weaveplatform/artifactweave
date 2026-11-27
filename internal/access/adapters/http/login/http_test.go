package login

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
)

type fakeLogin struct {
	err      error
	redirect string
	page     accessapi.NativePage
}

func (f *fakeLogin) Start(context.Context, accessapi.NativeRequest) (accessapi.NativeStart, error) {
	return accessapi.NativeStart{Browser: "browser-handle", Poll: "poll-handle"}, f.err
}
func (f *fakeLogin) Open(string, string) (accessapi.NativePage, error) { return f.page, f.err }
func (f *fakeLogin) Login(context.Context, string, string, string, string, string) (accessapi.NativePage, error) {
	return f.page, f.err
}

func (f *fakeLogin) Consent(context.Context, string, string, string, bool) (string, error) {
	return f.redirect, f.err
}
func (f *fakeLogin) Logout(string, string, string) error { return f.err }
func (f *fakeLogin) Poll(context.Context, string, string) (accessapi.IssuedToken, error) {
	return accessapi.IssuedToken{AccessToken: "token"}, f.err
}

func (f *fakeLogin) Exchange(context.Context, accessapi.NativeExchange) (accessapi.IssuedToken, error) {
	return accessapi.IssuedToken{AccessToken: "token", ExpiresIn: 3600}, f.err
}

func request(t *testing.T, h Handler, method, path, body, origin string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	h.Mount(router)
	r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestUnitNativeLoginHTTP(t *testing.T) {
	f := &fakeLogin{page: accessapi.NativePage{Session: "cookie", CSRF: "csrf", User: "<script>alert(1)</script>", Client: "npm", Repository: "<img>"}}
	h := Handler{Service: f, Origin: "https://login.example"}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/artifactory/api/npm/npm/-/v1/login", "{}", 200},
		{"POST", "/artifactory/api/npm/npm/-/v1/login", "not-json", 400},
		{"POST", "/artifactory/api/npm/npm/-/v1/login", "null", 400},
		{"POST", "/artifactory/api/npm/npm/-/v1/login", `{} {}`, 400},
		{"POST", "/artifactory/api/npm/npm/-/v1/login", `{"create":true}`, 400},
		{"POST", "/artifactory/api/npm/npm/-/v1/login", `{"payload":"` + strings.Repeat("x", 8192) + `"}`, 400},
		{"GET", "/artifactory/api/npm/npm/-/v1/done?sessionId=poll", "", 200},
		{"GET", pagePath + "?request=b", "", 200},
		{"POST", pagePath + "?request=b", "action=login&csrf=csrf", 200},
		{"POST", pagePath + "?request=b", "action=approve&csrf=csrf", 200},
		{"POST", pagePath + "?request=b", "action=deny&csrf=csrf", 200},
		{"POST", pagePath + "?request=b", "action=logout&csrf=csrf", 200},
		{"POST", pagePath + "?request=b", "action=unknown", 400},
		{"GET", authorizePath + "?response_type=code", "", 303},
		{"GET", authorizePath, "", 400},
		{"POST", tokenPath, "grant_type=authorization_code", 200},
		{"POST", tokenPath, "grant_type=refresh_token", 400},
		{"POST", tokenPath, "grant_type=authorization_code&client_secret=secret", 400},
		{"HEAD", "/artifactory/api/npm/npm/-/v1/done", "", 405},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			w := request(t, h, tc.method, tc.path, tc.body, h.Origin)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body)
			}
			if tc.status != 405 && (w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer") {
				t.Fatal("unsafe cache/referrer headers")
			}
			if strings.Contains(w.Body.String(), "<script>") || strings.Contains(w.Body.String(), "<img>") {
				t.Fatal("unescaped HTML")
			}
		})
	}
	if w := request(t, h, "POST", pagePath, "action=approve", "https://evil.example"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, err := range []error{accessapi.ErrNativeLogin, accessapi.ErrNativeCapacity, accessapi.ErrNativePending} {
		f.err = err
		for _, tc := range []struct{ method, path, body string }{{"GET", pagePath, ""}, {"POST", pagePath, "action=login"}, {"POST", pagePath, "action=approve"}, {"POST", pagePath, "action=logout"}, {"POST", tokenPath, "grant_type=authorization_code"}, {"POST", "/artifactory/api/npm/npm/-/v1/login", "{}"}, {"GET", authorizePath + "?response_type=code", ""}, {"GET", "/artifactory/api/npm/npm/-/v1/done", ""}} {
			w := request(t, h, tc.method, tc.path, tc.body, h.Origin)
			want := 400
			if err == accessapi.ErrNativeCapacity && tc.path != tokenPath {
				want = 429
			}
			if err == accessapi.ErrNativePending && strings.HasSuffix(tc.path, "/done") {
				want = 202
				if w.Header().Get("Retry-After") != "5" {
					t.Fatal("missing poll delay")
				}
			}
			if w.Code != want {
				t.Fatal(tc, w.Code, w.Body)
			}
		}
	}
	f.err = nil
	f.redirect = "http://localhost:10000/login?code=c&state=s"
	if w := request(t, h, "POST", pagePath, "action=approve", h.Origin); w.Code != 303 || w.Header().Get("Location") != f.redirect {
		t.Fatal(w.Code, w.Header())
	}
	var decoded map[string]string
	w := request(t, h, "POST", "/artifactory/api/npm/npm/-/v1/login", "{}", h.Origin)
	if json.Unmarshal(w.Body.Bytes(), &decoded) != nil || !strings.HasPrefix(decoded["loginUrl"], h.Origin) {
		t.Fatal(w.Body)
	}
}

func TestUnitNativeLoginPublicBoundary(t *testing.T) {
	h := Handler{Service: &fakeLogin{}, Origin: "https://login.example"}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{{"GET", pagePath, true}, {"POST", pagePath, true}, {"GET", authorizePath, true}, {"POST", tokenPath, true}, {"POST", "/artifactory/api/npm/npm/-/v1/login", true}, {"GET", "/artifactory/api/npm/npm/-/v1/done", true}, {"HEAD", "/artifactory/api/npm/npm/-/v1/done", false}, {"PUT", pagePath, false}, {"GET", "/artifactory/api/npm/npm/-/ping", false}, {"POST", "/artifactory/api/npm//-/v1/login", false}, {"GET", "/other", false}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := h.Public(r); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	for _, off := range []Handler{{}, {Service: &fakeLogin{}, Origin: "http://evil.example"}} {
		if off.Public(httptest.NewRequest("GET", pagePath, nil)) {
			t.Fatal("disabled bypass")
		}
		if w := request(t, off, "GET", pagePath, "", ""); w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
}

func TestUnitNativeLoginCanonicalOriginBoundary(t *testing.T) {
	h := Handler{Service: &fakeLogin{}, Origin: "https://login.example"}
	for _, origin := range []string{"https://login.example:443", "https://LOGIN.example", "http://login.example", "null", ""} {
		if w := request(t, h, "POST", pagePath, "action=login", origin); w.Code != 400 {
			t.Fatal("nonexact Origin accepted", origin, w.Code)
		}
	}
	if w := request(t, h, "POST", pagePath, "action=login", h.Origin); w.Code != 200 {
		t.Fatal("canonical Origin refused", w.Code)
	}
	for _, bad := range []string{"https://login.example:443", "https://bücher.example", "https://127.1", "https://[2001:0db8::1]"} {
		h.Origin = bad
		if h.Enabled() {
			t.Fatal("noncanonical configuration enabled", bad)
		}
	}
	h.Origin = "https://login.example"
	router := chi.NewRouter()
	h.Mount(router)
	r := httptest.NewRequest("POST", "/artifactory/api/npm/npm/-/v1/login", strings.NewReader(`{}`))
	r.Host = "attacker.example"
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	r.Header.Set("X-JFrog-Override-Base-Url", "https://attacker.example")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "attacker.example") {
		t.Fatal("request-selected credential origin", w.Code, w.Body)
	}
}
