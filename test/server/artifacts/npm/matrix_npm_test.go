package npm_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
)

// npmMatrixPart is npm's share of the route × principal matrix, which
// its pull requests (#286–#294) fill in. Package work lies under the
// writer's grants and lib outside them, so a packument is held to the
// same grants as its tarball.
var npmMatrixPart = matrixPart{
	rows: func() []matrixRow {
		local := npmBase + "npm-local/"
		reads := func(name string) rule {
			return holds("npm-local", npmfmt.CanonicalPath(name, "1.0.0"), api.ActionRead)
		}
		return npmVirtualMatrixRows([]matrixRow{
			// Manage on the repository, which only the administrators hold
			// here (#293).
			row(npmReindex, local+"reindex", "", holds("npm-local", "", api.ActionManage), 200),
			row(npmPackument, local+"work", "", reads("work"), 200),
			row(npmPackument, npmBase+"npm-virtual/work", "", reads("work"), 200),
			row(npmManifest, npmBase+"npm-virtual/lib/1.0.0", "", reads("lib"), 200),
			row(npmTarball, npmBase+"npm-virtual/lib/-/lib-1.0.0.tgz", "", reads("lib"), 200),
			row(npmPing, npmBase+"npm-virtual/-/ping", "", anyone, 200),
			row(npmPackument, local+"lib", "", reads("lib"), 200),
			row(npmManifest, local+"work/1.0.0", "", reads("work"), 200),
			row(npmManifest, local+"lib/latest", "", reads("lib"), 200),
			row(npmTarball, local+"work/-/work-1.0.0.tgz", "", reads("work"), 200),
			row(npmTarball, local+"lib/-/lib-1.0.0.tgz", "", reads("lib"), 200),
			row(npmTarballHead, local+"lib/-/lib-1.0.0.tgz", "", reads("lib"), 200),
			row(npmPing, local+"-/ping", "", anyone, 200),
			row(npmWhoami, local+"-/whoami", "", signedIn, 200),
			// Each principal publishes to a fixture of its own: the
			// version is published once.
			row(npmPublish, local+"work", npmPublishBody("work", "2.0.0"),
				holds("npm-local", npmfmt.CanonicalPath("work", "2.0.0"), api.ActionWrite),
				201).alone(),
			// npm-releases handles no snapshots: a caller who may deploy a
			// pre-release meets the policy, the rest are refused first.
			row(npmPublish, npmBase+"npm-releases/work", npmPublishBody("work", "2.0.0-rc.1"),
				holds("npm-releases", npmfmt.CanonicalPath("work", "2.0.0-rc.1"),
					api.ActionWrite), 409),
		})
	},
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		for _, name := range []string{"work", "lib"} {
			f.deploy(t, "npm-local", npmfmt.CanonicalPath(name, "1.0.0"),
				npmTarballOf(name, "1.0.0"))
		}
	},
}

// A virtual uses the same permission rules as these local rows; writes name
// the deployment target while searches filter its effective members.
// Fresh fixtures keep each repeated mutation independent of its local control.
func npmVirtualMatrixRows(rows []matrixRow) []matrixRow {
	out := append([]matrixRow(nil), rows...)
	for _, r := range rows {
		switch r.route {
		case npmSearch, npmPublish, npmTagAdd, npmTagRemove, npmUnpublishVersions, npmUnpublishTarball, npmUnpublish, npmLogin, npmLogout, npmWhoami:
		default:
			continue
		}
		if !strings.HasPrefix(r.path, npmBase+"npm-local/") {
			continue
		}
		r.path = strings.Replace(r.path, npmBase+"npm-local/", npmBase+"npm-virtual/", 1)
		out = append(out, r.alone())
	}
	return out
}
