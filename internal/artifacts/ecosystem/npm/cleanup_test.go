package npm_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
)

func TestUnitCleanupLeadKeepsScopedCanonicalTarball(t *testing.T) {
	selectLead, err := (npm.Handler{}).CleanupLead(model.PackageVersion{})
	if err != nil {
		t.Fatal(err)
	}
	version := api.IndexVersion{Package: api.IndexPackage{Name: "@scope/name"}, Version: "1.2.0-beta.1"}
	const canonical = "@scope/name/-/@scope/name-1.2.0-beta.1.tgz"
	for _, p := range []string{canonical, "@scope/name/-/name-1.2.0-beta.1.tgz", canonical + ".sha256", "package.json"} {
		lead := selectLead(api.IndexFile{Version: version, Path: p})
		if lead.Found != (p == canonical) || !lead.Single {
			t.Fatalf("scoped tarball classification for %s = %+v", p, lead)
		}
	}
}
