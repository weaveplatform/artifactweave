package server

import (
	"net/http"
	"testing"
)

// HEAD reaches the protocol kit while GET reaches the generated strict
// operation. Neither route may shadow the other when their names coincide.
func TestPypiJSONRoutes(t *testing.T) {
	h := statusFixture(t, false)
	for _, suffix := range []string{"json", "1.0/json"} {
		path := pypiBase + "pypi-local/pypi/Status_Pkg/" + suffix
		get := do(t, h, http.MethodGet, path, basic("admin", "pw"), "")
		head := do(t, h, http.MethodHead, path, basic("admin", "pw"), "")
		wantStatus(t, get, http.StatusOK)
		wantStatus(t, head, http.StatusOK)
		for _, name := range []string{"Content-Type", "Content-Length"} {
			if got, want := head.Header().Get(name), get.Header().Get(name); got != want {
				t.Errorf("%s: HEAD %s=%q, GET=%q", path, name, got, want)
			}
		}
		if head.Body.Len() != 0 {
			t.Fatal("HEAD returned JSON bytes")
		}
		doc := decodeObject(t, get.Body.String())
		if doc["info"] == nil || doc["urls"] == nil {
			t.Fatalf("JSON document: %v", doc)
		}
	}
}
