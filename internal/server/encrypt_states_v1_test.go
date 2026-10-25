package server

import (
	"net/http"
	"strings"
	"testing"
)

// The v1 read of a local repository of any package type sends
// encryptStates, as 7.x's read of a Maven repository does (RTFACT-31100);
// the v2 read keeps it for the Terraform types alone.
func TestAccEncryptStatesInTheV1Read(t *testing.T) {
	w := newItemWorld(t, false)
	v1 := w.do(http.MethodGet, "/artifactory/api/repositories/libs-local", "admin", "")
	wantStatus(t, v1, http.StatusOK)
	if !strings.Contains(v1.Body.String(), `"encryptStates":true`) {
		t.Fatalf("the v1 read of a generic local repository: %s", v1.Body)
	}
	v2 := w.do(http.MethodGet, "/artifactory/api/v2/repositories/libs-local", "admin", "")
	wantStatus(t, v2, http.StatusOK)
	if strings.Contains(v2.Body.String(), "encryptStates") {
		t.Fatalf("the v2 read of a generic local repository: %s", v2.Body)
	}
	remote := w.do(http.MethodGet, "/artifactory/api/repositories/up", "admin", "")
	if strings.Contains(remote.Body.String(), "encryptStates") {
		t.Fatalf("the v1 read of a remote repository: %s", remote.Body)
	}
}
