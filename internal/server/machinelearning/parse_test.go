package machinelearning

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

const validManifest = `{"created_date":"2026-10-28T12:00:00.12","model_artifacts":[{"artifact_path":"model.pkl","size":0,"checksums":{"sha2":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}}],"model_format":{"framework":"files","framework_version":"","serialization_format":"pkl","runtime":"python","runtime_version":"3.13"}}`

func TestUnitModelCommitParsing(t *testing.T) {
	type field struct{ name, value string }
	many := map[string]string{}
	for i := range 257 {
		many[strings.Repeat("x", i+1)] = "y"
	}
	manyJSON, _ := json.Marshal(many)
	for _, tc := range []struct {
		name   string
		fields []field
		valid  bool
	}{
		{"SDK null properties", []field{{"modelManifest", validManifest}, {"additionalData", "null"}}, true},
		{"missing manifest", []field{{"additionalData", "{}"}}, false},
		{"duplicate manifest", []field{{"modelManifest", validManifest}, {"modelManifest", validManifest}}, false},
		{"unknown part", []field{{"unexpected", "{}"}}, false},
		{"invalid manifest", []field{{"modelManifest", "{}"}}, false},
		{"nonstring property", []field{{"modelManifest", validManifest}, {"additionalData", `{"key":42}`}}, false},
		{"too many properties", []field{{"modelManifest", validManifest}, {"additionalData", string(manyJSON)}}, false},
		{"oversized property key", []field{{"modelManifest", validManifest}, {"additionalData", `{"` + strings.Repeat("x", 256) + `":"y"}`}}, false},
		{"oversized property value", []field{{"modelManifest", validManifest}, {"additionalData", `{"key":"` + strings.Repeat("x", 4097) + `"}`}}, false},
		{"oversized properties", []field{{"modelManifest", validManifest}, {"additionalData", strings.Repeat(" ", 65537)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for _, f := range tc.fields {
				if err := writer.WriteField(f.name, f.value); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("PUT", "/", &body)
			r.Header.Set("Content-Type", writer.FormDataContentType())
			_, _, err := readCommit(httptest.NewRecorder(), r)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got %v", tc.valid, err)
			}
		})
	}
	for _, body := range []string{"broken multipart", "--boundary\r\nContent-Disposition: form-data; name=\"modelManifest\"\r\n\r\n" + validManifest} {
		r := httptest.NewRequest("PUT", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
		if _, _, err := readCommit(httptest.NewRecorder(), r); err == nil {
			t.Fatal("accepted truncated multipart")
		}
	}
}

func TestUnitModelIdentityRejectsInvalidEscapes(t *testing.T) {
	for _, key := range []string{"name", "namespace", "version"} {
		r := httptest.NewRequest("GET", "/", nil)
		rc := chi.NewRouteContext()
		for _, k := range []string{"name", "namespace", "version"} {
			value := "valid"
			if k == key {
				value = "%invalid"
			}
			rc.URLParams.Add(k, value)
		}
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
		if _, err := identity(r, false); err == nil {
			t.Fatalf("accepted malformed %s", key)
		}
	}
}
