package apierror

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteEnvelopes(t *testing.T) {
	cases := []struct {
		name    string
		surface Surface
		err     error
		status  int
		want    string
	}{
		{
			"artifactory",
			Artifactory,
			New(404, "not found"),
			404,
			`{"errors":[{"message":"not found","status":404}]}`,
		},
		{
			"access",
			Access,
			New(409, "exists"),
			409,
			`{"errors":[{"code":"CONFLICT","message":"exists"}]}`,
		},
		{
			"oci",
			OCI,
			&Error{
				Status:  404,
				Code:    "BLOB_UNKNOWN",
				Message: "blob unknown",
				Detail:  map[string]string{"digest": "sha256:abc"},
			},
			404,
			`{"errors":[{"code":"BLOB_UNKNOWN","detail":{"digest":"sha256:abc"},"message":"blob unknown"}]}`,
		},
		{
			"oci no detail",
			OCI,
			&Error{Status: 400, Code: "NAME_INVALID", Message: "bad name"},
			400,
			`{"errors":[{"code":"NAME_INVALID","message":"bad name"}]}`,
		},
		{
			"xray",
			Xray,
			NotSupported("Xray"),
			501,
			`{"error":"Xray is not supported by artifactweave"}`,
		},
		{"npm", NPM, New(401, "unauthorized"), 401, `{"error":"unauthorized"}`},
		{
			"terraform",
			TerraformRegistry,
			New(404, "no such provider"),
			404,
			`{"errors":["no such provider"]}`,
		},
		{
			"distribution",
			Distribution,
			&Error{Status: 404, Code: "NOT_FOUND", Message: "no bundle", Detail: "x"},
			404,
			`{"detail":"x","errors":[{"message":"no bundle","status":404}],"message":"no bundle","status_code":404}`,
		},
		{
			"distribution no detail",
			Distribution,
			New(400, "bad"),
			400,
			`{"errors":[{"message":"bad","status":400}],"message":"bad","status_code":400}`,
		},
		{
			"artifactory legacy",
			ArtifactoryLegacy,
			New(400, "policy is running"),
			400,
			`{"error":"Bad Request","errors":[{"message":"policy is running","status":400}],"message":"policy is running"}`,
		},
		{"flat", Flat, New(409, "exists"), 409, `{"code":"CONFLICT","message":"exists"}`},
		{
			"flat details",
			Flat,
			&Error{Status: 400, Code: "INVALID", Message: "bad", Detail: []string{"name"}},
			400,
			`{"code":"INVALID","details":["name"],"message":"bad"}`,
		},
		{
			"unknown error",
			Artifactory,
			errors.New("boom"),
			500,
			`{"errors":[{"message":"internal error","status":500}]}`,
		},
		{
			"wrapped",
			Access,
			errors.Join(errors.New("ctx"), New(403, "denied")),
			403,
			`{"errors":[{"code":"FORBIDDEN","message":"denied"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Write(rec, tc.surface, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content type %q", ct)
			}
			var got, want any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
				t.Fatalf("body %s, want %s", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestWriteText(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, Text, New(http.StatusGone, "gone"))
	if rec.Code != http.StatusGone ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
		rec.Body.String() != "gone\n" {
		t.Fatalf(
			"unexpected text response: %d %q %q",
			rec.Code,
			rec.Header().Get("Content-Type"),
			rec.Body.String(),
		)
	}
}

func TestDefaultCodes(t *testing.T) {
	for status, code := range map[int]string{400: "BAD_REQUEST", 401: "UNAUTHORIZED", 403: "FORBIDDEN", 404: "NOT_FOUND", 409: "CONFLICT", 501: "UNSUPPORTED", 418: "INTERNAL_ERROR"} {
		if got := New(status, "x").Code; got != code {
			t.Errorf("status %d: code %q, want %q", status, got, code)
		}
	}
	if New(404, "m").Error() != "m" {
		t.Fatal("Error() should return the message")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
