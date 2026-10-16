package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func testHandler() http.Handler {
	return New(
		Config{
			BaseURL:       "https://cfg.example.com",
			CompatVersion: "7.155.0",
			Revision:      "v0.1.0-3-gabc",
		},
	)
}

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func decode(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

// TestSystemLicense reads the license as an administrator, the only caller
// it answers.
func TestSystemLicense(t *testing.T) {
	rec := do(t, statusFixture(t, false), http.MethodGet, "/artifactory/api/system/license",
		basic("admin", "pw"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	want := `{"type":"artifactweave","validThrough":"never","licensedTo":"artifactweave"}`
	if diff := cmp.Diff(decode(t, want), decode(t, rec.Body.String())); diff != "" {
		t.Errorf("body (-want +got):\n%s", diff)
	}
}

func TestSystemEndpoints(t *testing.T) {
	h := testHandler()
	cases := []struct {
		name        string
		method      string
		path        string
		headers     map[string]string
		status      int
		contentType string
		text        string
		jsonBody    string
	}{
		{
			"artifactory ping",
			"GET",
			"/artifactory/api/system/ping",
			nil,
			200,
			"text/plain; charset=utf-8",
			"OK",
			"",
		},
		{
			"access ping",
			"GET",
			"/access/api/v1/system/ping",
			nil,
			200,
			"text/plain; charset=utf-8",
			"OK",
			"",
		},
		{
			"version",
			"GET",
			"/artifactory/api/system/version",
			nil,
			200,
			"application/json",
			"",
			`{"version":"7.155.0","revision":"v0.1.0-3-gabc","addons":[],"license":"artifactweave"}`,
		},
		{
			"version vendor accept",
			"GET",
			"/artifactory/api/system/version",
			map[string]string{"Accept": versionMediaType},
			200,
			versionMediaType,
			"",
			`{"version":"7.155.0","revision":"v0.1.0-3-gabc","addons":[],"license":"artifactweave"}`,
		},
		{
			"readiness",
			"GET",
			"/artifactory/api/v1/system/readiness",
			nil,
			200,
			"application/json",
			"",
			`{"code":"OK"}`,
		},
		{
			"liveness",
			"GET",
			"/artifactory/api/v1/system/liveness",
			nil,
			200,
			"application/json",
			"",
			`{"code":"OK"}`,
		},
		{
			"access version",
			"GET",
			"/access/api/v1/system/version",
			nil,
			200,
			"application/json",
			"",
			`{"name":"7.155.0","version":"7.155.0"}`,
		},
		{
			"router health",
			"GET",
			"/router/api/v1/system/health",
			nil,
			200,
			"application/json",
			"",
			`{"router":{"state":"HEALTHY"},"services":[{"service_id":"jfrt@artifactweave","state":"HEALTHY"},{"service_id":"jfac@artifactweave","state":"HEALTHY"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, tc.headers, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != tc.contentType {
				t.Fatalf("content type %q, want %q", ct, tc.contentType)
			}
			if tc.text != "" && rec.Body.String() != tc.text {
				t.Fatalf("body %q, want %q", rec.Body.String(), tc.text)
			}
			if tc.jsonBody != "" {
				if diff := cmp.Diff(
					decode(t, tc.jsonBody),
					decode(t, rec.Body.String()),
				); diff != "" {
					t.Fatalf("body mismatch (-want +got):\n%s", diff)
				}
			}
			if rec.Header().Get(requestIDHeader) == "" {
				t.Fatal("missing request id header")
			}
		})
	}
}

func TestTelemetrySinks(t *testing.T) {
	h := testHandler()
	for _, path := range []string{"/artifactory/api/system/usage", "/jfconnect/api/v1/backoffice/metrics/log"} {
		rec := do(
			t,
			h,
			"POST",
			path,
			map[string]string{"Content-Type": "application/json"},
			`{"productId":"jfrog-cli-go/2.60.0","features":[]}`,
		)
		if rec.Code != 200 || rec.Body.Len() != 0 {
			t.Fatalf("%s: status %d body %q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestStubsAndEnvelopes(t *testing.T) {
	h := testHandler()
	cases := []struct {
		name   string
		method string
		path   string
		status int
		body   string
	}{
		{
			"xray 501 in xray envelope",
			"GET",
			"/xray/api/v1/system/version",
			501,
			`{"error":"Xray is not supported by artifactweave"}`,
		},
		{
			"lifecycle 501",
			"POST",
			"/lifecycle/api/v2/release_bundle",
			501,
			`{"errors":[{"status":501,"message":"Release Lifecycle Management is not supported by artifactweave"}]}`,
		},
		{
			"apptrust 501",
			"GET",
			"/apptrust/api/v1/applications",
			501,
			`{"errors":[{"status":501,"message":"AppTrust is not supported by artifactweave"}]}`,
		},
		{
			"evidence 501",
			"GET",
			"/evidence/api/v1/x",
			501,
			`{"errors":[{"status":501,"message":"Evidence is not supported by artifactweave"}]}`,
		},
		{
			"worker specified, unimplemented",
			"GET",
			"/worker/api/v1/workers",
			501,
			`{"errors":[{"code":"NOT_IMPLEMENTED","message":"not implemented by artifactweave yet"}]}`,
		},
		{
			"federation specified, unimplemented",
			"POST",
			"/artifactory/api/federation/fullSync/x",
			501,
			`{"errors":[{"status":501,"message":"not implemented by artifactweave yet"}]}`,
		},
		{
			"distribution specified, unimplemented",
			"GET",
			"/distribution/api/v1/release_bundle",
			501,
			`{"status_code":501,"message":"not implemented by artifactweave yet","errors":[{"status":501,"message":"not implemented by artifactweave yet"}]}`,
		},
		{
			"mc 404",
			"GET",
			"/mc/api/v1/jpds",
			404,
			`{"errors":[{"status":404,"message":"not found: GET /mc/api/v1/jpds"}]}`,
		},
		{
			"grid specified, unimplemented",
			"GET",
			"/grid/api/v1/topology",
			501,
			`{"code":"NOT_IMPLEMENTED","message":"not implemented by artifactweave yet"}`,
		},
		{
			"distribution unknown path",
			"GET",
			"/distribution/api/v9/nothing",
			404,
			`{"status_code":404,"message":"not found: GET /distribution/api/v9/nothing","errors":[{"status":404,"message":"not found: GET /distribution/api/v9/nothing"}]}`,
		},
		{
			"pfed unknown path",
			"GET",
			"/pfed/api/v9/nothing",
			404,
			`{"code":"NOT_FOUND","message":"not found: GET /pfed/api/v9/nothing"}`,
		},
		{
			"artifactory 404",
			"GET",
			"/artifactory/api/nonexistent/nope",
			404,
			`{"errors":[{"status":404,"message":"not found: GET /artifactory/api/nonexistent/nope"}]}`,
		},
		{
			"access 404",
			"GET",
			"/access/api/v9/nothing",
			404,
			`{"errors":[{"code":"NOT_FOUND","message":"not found: GET /access/api/v9/nothing"}]}`,
		},
		{
			"oci 404",
			"GET",
			"/v2/nope/manifests/latest",
			404,
			`{"errors":[{"code":"NAME_UNKNOWN","message":"repository name not known to registry: nope"}]}`,
		},
		{
			"docker api 404",
			"GET",
			"/artifactory/api/docker/nope/v2/img/manifests/latest",
			404,
			`{"errors":[{"code":"NAME_UNKNOWN","message":"repository name not known to registry: nope"}]}`,
		},
		{
			"npm 404",
			"GET",
			"/artifactory/api/npm/nope/left-pad",
			404,
			`{"error":"not found: GET /artifactory/api/npm/nope/left-pad"}`,
		},
		{
			"terraform 404",
			"GET",
			"/artifactory/api/terraform/v1/providers/x/y/versions",
			404,
			`{"errors":["not found: GET /artifactory/api/terraform/v1/providers/x/y/versions"]}`,
		},
		{
			"root 404",
			"GET",
			"/nothing",
			404,
			`{"errors":[{"status":404,"message":"not found: GET /nothing"}]}`,
		},
		{
			"method not allowed",
			"DELETE",
			"/artifactory/api/system/ping",
			405,
			`{"errors":[{"status":405,"message":"method not allowed: DELETE /artifactory/api/system/ping"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, nil, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if diff := cmp.Diff(decode(t, tc.body), decode(t, rec.Body.String())); diff != "" {
				t.Fatalf("body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTextSurfaces404(t *testing.T) {
	h := testHandler()
	for _, path := range []string{"/artifactory/api/go/nope/github.com/x/y/@v/list", "/artifactory/api/pypi/nope/simple/x/"} {
		rec := do(t, h, "GET", path, nil, "")
		if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

func TestNewUsesConfiguredLogger(t *testing.T) {
	var buf bytes.Buffer
	h := New(Config{
		CompatVersion: "7.155.0",
		Logger:        slog.New(slog.NewJSONHandler(&buf, nil)),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/artifactory/api/system/ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(buf.String(), `"path":"/artifactory/api/system/ping"`) {
		t.Fatalf("access log did not go to the configured logger: %q", buf.String())
	}
}
