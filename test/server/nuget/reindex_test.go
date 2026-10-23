package nuget_test

import (
	"net/http"
	"testing"
)

// The generated reindex operation remains authoritative at the v2 root.
// Exercise it on the full router, since the protocol-only fixture has no
// generated operation to reject unsupported methods.
func TestNuGetReindexMethod(t *testing.T) {
	h, _, _ := nugetServer(t)
	rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/nuget/nuget-local/reindex",
		basic("admin", "pw"),
		"",
	)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("reindex method: %d %s", rec.Code, rec.Body)
	}
}
