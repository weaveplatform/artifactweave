package artifactshttp

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/search"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// Without an authenticator the paths a search would disclose cannot be
// rechecked against its caller's current permissions, so such a search is
// refused; one that discloses nothing has nothing to recheck.
func TestUnitSearchUpstreamDisclosuresWithoutAuthenticator(t *testing.T) {
	u := &searchRoutes{r: &searchRun{
		SearchRun: &search.SearchRun{Principal: kernel.Principal{Name: "admin", Password: true}},
		s:         newArtifactoryAPI(withAccess(t, Config{Store: inmem.New()})),
	}}
	if err := u.CheckDisclosures(t.Context()); err != nil {
		t.Fatalf("a search that discloses nothing: %v", err)
	}
	u.disclosures = []searchDisclosure{{key: "remote", path: "org/lib/1.0/lib-1.0.jar"}}
	if err := u.CheckDisclosures(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("a search that discloses a path: %v, want source.ErrDenied", err)
	}
}
