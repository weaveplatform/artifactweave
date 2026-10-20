package server

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Pages require some Read grant in the repository and then filter files;
// downloads and computed metadata require Read on their exact wheel path.
var pypiMatrixPart = matrixPart{
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		seedPypi(t, f.items, f.db, pypiWheelPath, "status-pkg")
		seedPypi(t, f.items, f.db, "work/work-1.0-py3-none-any.whl", "work")
	},
	tune: pypiStatusPart.tune,
	rows: func() []matrixRow {
		local := pypiBase + "pypi-local/"
		reads := holds("pypi-local", pypiWheelPath, types.ActionRead)
		return []matrixRow{
			row(pypiSimple, local+"simple", "", anyone, 302),
			row(pypiSimpleHead, local+"simple", "", anyone, 302),
			row(pypiList, local+"simple/", "", pypiLists, 200),
			row(pypiListHead, local+"simple/", "", pypiLists, 200),
			row(pypiProject, local+"simple/status-pkg/", "", pypiProjectReads, 200),
			row(pypiProjectHead, local+"simple/status-pkg/", "", pypiProjectReads, 200),
			row(pypiFile, local+pypiWheelPath, "", reads, 200),
			row(pypiFileHead, local+pypiWheelPath, "", reads, 200),
			row(pypiMetadata, local+pypiWheelPath+".metadata", "", reads, 200),
			row(pypiMetadataHead, local+pypiWheelPath+".metadata", "", reads, 200),
		}
	},
}

func pypiLists(p matrixPrincipal) int {
	if p.admin {
		return 0
	}
	for _, g := range p.grants {
		if (g.repo == "pypi-local" || g.repo == "*") && g.actions&types.ActionRead != 0 {
			return 0
		}
	}
	return refuse(p)
}

func pypiProjectReads(p matrixPrincipal) int {
	if status := pypiLists(p); status != 0 {
		return status
	}
	if p.holds("pypi-local", pypiWheelPath, types.ActionRead) {
		return 0
	}
	return http.StatusNotFound
}
