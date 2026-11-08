package helm_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// helmMatrixPart is Helm's share of the route × principal matrix (#548): a
// delete, copy or move of a chart takes its .prov along, so the caller
// needs on the .prov the rights it needs on the chart (decision 0039).
// charter holds them on every chart of helm-local and on no .prov.
var helmMatrixPart = matrixPart{
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		grantCharter(t, f.db)
	},
	rows: func() []matrixRow {
		const (
			del   = "DELETE /artifactory/{repoKey}/{filePath}"
			cp    = "POST /artifactory/api/copy/{srcRepoKey}/{srcFilePath}"
			mv    = "POST /artifactory/api/move/{srcRepoKey}/{srcFilePath}"
			chart = "work/{p}/web-1.0.0.tgz"
			bare  = "work/{p}/bare-1.0.0.tgz"
			to    = "work/{p}-to/web-1.0.0.tgz"
		)
		read, write, del3 := types.ActionRead, types.ActionWrite, types.ActionDelete
		withProv := helmSeeded(true, chart)
		return []matrixRow{
			row(del, "/artifactory/helm-local/"+chart, "",
				holdsAll("helm-local", del3, chart, chart+".prov"), 204).seeding(withProv),
			row(del, "/artifactory/helm-local/"+bare, "",
				holdsAll("helm-local", del3, bare), 204).seeding(helmSeeded(false, bare)),
			row(cp, "/artifactory/api/copy/helm-local/"+chart+"?to=/helm-local/"+to, "",
				both(holdsAll("helm-local", read, chart, chart+".prov"),
					holdsAll("helm-local", write, to, to+".prov")), 200).seeding(withProv),
			row(mv, "/artifactory/api/move/helm-local/"+chart+"?to=/helm-local/"+to, "",
				both(holdsAll("helm-local", read|del3, chart, chart+".prov"),
					holdsAll("helm-local", write, to, to+".prov")), 200).seeding(withProv),
		}
	},
}

// helmSeeded seeds a chart at each path in helm-local, {p} the principal's
// name, with its .prov beside it when prov is set.
func helmSeeded(prov bool, paths ...string) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		for _, path := range paths {
			path = strings.ReplaceAll(path, "{p}", p)
			f.deploy(t, "helm-local", path, helmChart("web", "1.0.0"))
			if prov {
				f.deploy(t, "helm-local", path+".prov", []byte("signed"))
			}
		}
	}
}

// holdsAll: a caller holding every action in want at each path in repoKey.
func holdsAll(repoKey string, want types.Actions, paths ...string) rule {
	return func(p matrixPrincipal) int {
		for _, path := range paths {
			if s := holds(repoKey, path, want)(p); s != 0 {
				return s
			}
		}
		return 0
	}
}

// both: a caller a and b allow.
func both(a, b rule) rule {
	return func(p matrixPrincipal) int {
		if s := a(p); s != 0 {
			return s
		}
		return b(p)
	}
}
