package server

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// Without an authenticator the paths a search would disclose cannot be
// rechecked against its caller's current permissions, so such a search is
// refused; one that discloses nothing has nothing to recheck.
func TestUnitSearchUpstreamDisclosuresWithoutAuthenticator(t *testing.T) {
	u := &upstreamSearch{r: &searchRun{
		s: newArtifactoryService(withAccess(Config{Store: inmem.New()})),
		p: kernel.Principal{Name: "admin", Password: true},
	}}
	if err := u.checkDisclosures(t.Context()); err != nil {
		t.Fatalf("a search that discloses nothing: %v", err)
	}
	u.disclosures = []searchDisclosure{{key: "remote", path: "org/lib/1.0/lib-1.0.jar"}}
	if err := u.checkDisclosures(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("a search that discloses a path: %v, want source.ErrDenied", err)
	}
}
