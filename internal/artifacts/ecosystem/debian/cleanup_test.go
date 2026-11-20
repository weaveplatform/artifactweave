package debian_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
)

func TestUnitCleanupLeadPreservesBinaryArchitectures(t *testing.T) {
	selectLead, err := (debian.Handler{}).CleanupLead(model.PackageVersion{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		found bool
	}{
		{"pool/main/p/pkg/pkg_1.0-10_amd64.deb", true},
		{"pool/main/p/pkg/pkg_1.0-10_arm64.deb", true},
		{"pool/main/p/pkg/pkg-dbgsym_1.0-10_amd64.ddeb", true},
		{"pool/main/p/pkg/pkg_1.0-10.dsc", false},
		{"pool/main/p/pkg/pkg_1.0-10.tar.gz", false},
		{"../outside.deb", false},
	} {
		lead := selectLead(api.IndexFile{Path: tc.path})
		if lead.Found != tc.found || lead.Single {
			t.Fatalf("binary lead classification for %s = %+v", tc.path, lead)
		}
	}
}
