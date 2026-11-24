package federationhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/pairing"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// fakeTrust records the calls the endpoints make and answers err.
type fakeTrust struct {
	err   error
	calls []string
}

func (f *fakeTrust) Issue(_ context.Context, use model.UseCase, base string) (string, error) {
	f.calls = append(f.calls, "issue "+string(use)+" "+base)
	return "tok", f.err
}

func (f *fakeTrust) Redeem(_ context.Context, use model.UseCase, token, _ string) error {
	f.calls = append(f.calls, "redeem "+string(use)+" "+token)
	return f.err
}

func (f *fakeTrust) Pairings(_ context.Context, use model.UseCase) ([]string, error) {
	f.calls = append(f.calls, "pairings "+string(use))
	return []string{"https://eu.example"}, f.err
}

func (f *fakeTrust) Unpair(_ context.Context, use model.UseCase, base string, force bool) error {
	f.calls = append(f.calls, fmt.Sprintf("unpair %s %s %t", use, base, force))
	return f.err
}

// trustRouter serves the endpoints with the caller's authority read from
// the X-Role header: admin passes, user is forbidden, none is anonymous.
func trustRouter(f *fakeTrust, withStub bool) http.Handler {
	api := ServiceTrustAPI{
		Pairing: f,
		Authorize: func(ctx context.Context) error {
			switch ctx.Value(roleKey{}) {
			case "admin":
				return nil
			case "user":
				return kernel.ErrForbidden
			default:
				return kernel.ErrUnauthenticated
			}
		},
	}
	if withStub {
		api.Unimplemented = func(method, template string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotImplemented)
				_, _ = w.Write([]byte(method + " " + template))
			})
		}
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware("https://us.example/artifactory"))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), roleKey{}, r.Header.Get("X-Role"))))
		})
	})
	api.RegisterRoutes(r)
	return r
}

type roleKey struct{}

func call(t *testing.T, h http.Handler, method, path, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Role", role)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUnitServiceTrustAnswers(t *testing.T) {
	const (
		pairingPath = "/artifactory/api/v1/service_trust/pairing/"
		listPath    = "/artifactory/api/v1/service_trust/pairings/"
		revokePath  = "/artifactory/api/v1/service_trust/revoke/"
		accessPath  = "/access/api/v1/service_trust/pairing/"
		exchange    = "/artifactory/api/v1/service_trust/exchange"
	)
	f := &fakeTrust{}
	h := trustRouter(f, true)
	cases := []struct {
		method, path, role, body string
		status                   int
		want                     string
	}{
		{"POST", pairingPath + "federated-repo/*", "admin", "", 200, `{"pairing_token":"tok"}`},
		{"POST", pairingPath + "federated-repo%2Fr", "admin", "", 200, `{"pairing_token":"tok"}`},
		{"POST", accessPath + "federated-repo/*", "admin", "", 200, `{"pairing_token":"tok"}`},
		{
			"PUT", pairingPath + "federated-repo/*", "admin", `{"pairing_token":"t"}`, 200,
			`{"message":"Trust with service federated-repo was established successfully"}`,
		},
		{
			"PUT", pairingPath + "federated-repo/r", "admin", `not json`, 200,
			`{"message":"Trust with service federated-repo was established successfully"}`,
		},
		{
			"DELETE", pairingPath + "federated-repo/*?force=true", "admin", `{"base_url":"https://eu.example"}`, 200,
			`{"message":"Trust with service federated-repo was canceled successfully"}`,
		},
		{"DELETE", pairingPath + "federated-repo/*?force=yes", "admin", `{"base_url":"https://eu.example"}`, 200, ""},
		{"GET", listPath + "federated-repo/*", "admin", "", 200, `[{"base_url":"https://eu.example"}]`},
		{"DELETE", revokePath + "federated-repo/r", "admin", "", 200, `{"message":"Token was successfully revoked"}`},
		{
			"POST", exchange, "", `{}`, 400,
			`{"message":"Error while token exchange. Peers of this server sign their requests and hold no token to exchange"}`,
		},
		{"POST", pairingPath + "federated-repo/*", "user", "", 403, ""},
		{"POST", pairingPath + "federated-repo/*", "", "", 401, ""},
		{"GET", listPath + "artifactory-cold", "admin", "", 404, `{"message":"Unknown use case"}`},
		{"GET", listPath + "artifactory-cold/ns", "admin", "", 404, `{"message":"Unknown use case"}`},
		{"PUT", pairingPath + "nothing", "user", "", 403, ""},
		{"DELETE", pairingPath + "nothing", "admin", `{"base_url":"x"}`, 404, `{"message":"Unknown use case"}`},
		{"DELETE", revokePath + "nothing", "", "", 401, ""},
		{"POST", pairingPath + "distribution", "", "", 501, "POST " + PairingTemplate},
		{"PUT", pairingPath + "mission-control", "", "", 501, "PUT " + PairingTemplate},
		{"GET", listPath + "bridge", "", "", 501, "GET " + PairingsTemplate},
		{"POST", accessPath + "platform-federation", "", "", 501, ""},
	}
	for _, c := range cases {
		rec := call(t, h, c.method, c.path, c.role, c.body)
		if rec.Code != c.status || c.want != "" && strings.TrimSpace(rec.Body.String()) != c.want {
			t.Errorf("%s %s as %q: %d %s, want %d %s", c.method, c.path, c.role, rec.Code, rec.Body, c.status, c.want)
		}
	}
	want := []string{
		"issue federated-repo/* https://us.example/artifactory",
		"issue federated-repo/r https://us.example/artifactory",
		"issue federated-repo/* https://us.example/artifactory",
		"redeem federated-repo/* t",
		"redeem federated-repo/r ",
		"unpair federated-repo/* https://eu.example true",
		"unpair federated-repo/* https://eu.example false",
		"pairings federated-repo/*",
	}
	if fmt.Sprint(f.calls) != fmt.Sprint(want) {
		t.Errorf("calls\n%v\nwant\n%v", f.calls, want)
	}
	// Without the stub handler a planned use case still answers 501.
	if rec := call(t, trustRouter(f, false), "POST", pairingPath+"distribution", "", ""); rec.Code != 501 {
		t.Errorf("planned use case without a stub: %d", rec.Code)
	}
	// Without an authority check nobody is admitted.
	api := ServiceTrustAPI{Pairing: f}
	r := chi.NewRouter()
	api.RegisterRoutes(r)
	if rec := call(t, r, "GET", listPath+"federated-repo/*", "admin", ""); rec.Code != 500 {
		t.Errorf("no authority check: %d", rec.Code)
	}
}

func TestUnitServiceTrustFailures(t *testing.T) {
	f := &fakeTrust{}
	h := trustRouter(f, true)
	const p = "/artifactory/api/v1/service_trust/"
	cases := []struct {
		err          error
		method, path string
		status       int
		want         string
	}{
		{
			&pairing.Refusal{Reason: "Attempt to pair instances with an expired token"}, "PUT", p + "pairing/federated-repo/*", 400,
			`{"message":"Error while token pairing. Attempt to pair instances with an expired token"}`,
		},
		{
			&pairing.Refusal{Reason: "Pairing does not exist for instance"}, "DELETE", p + "pairing/federated-repo/*", 400,
			`{"message":"Error while cancelling token pairing. Pairing does not exist for instance"}`,
		},
		{
			kernel.ErrStore, "POST", p + "pairing/federated-repo/*", 500,
			`{"message":"Error creating pairing token. Internal server error"}`,
		},
		{
			kernel.ErrStore, "GET", p + "pairings/federated-repo/*", 500,
			`{"message":"Error getting list of pairing tokens. Internal server error"}`,
		},
		{fmt.Errorf("wrapped: %w", kernel.ErrForbidden), "PUT", p + "pairing/federated-repo/*", 403, ""},
		{errors.Join(kernel.ErrBadCredentials), "DELETE", p + "pairing/federated-repo/r", 401, ""},
	}
	for _, c := range cases {
		f.err = c.err
		rec := call(t, h, c.method, c.path, "admin", "{}")
		if rec.Code != c.status || c.want != "" && strings.TrimSpace(rec.Body.String()) != c.want {
			t.Errorf("%v on %s %s: %d %s, want %d %s", c.err, c.method, c.path, rec.Code, rec.Body, c.status, c.want)
		}
	}
}
