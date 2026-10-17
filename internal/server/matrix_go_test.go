package server

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// goMatrixPart is Go's share of the route × principal matrix, which
// its pull requests (#272–#275) fill in.
var goMatrixPart = matrixPart{
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		seedGoModule(t, f.items, f.db)
	},
	rows: func() []matrixRow {
		m := goBase + "go-local/" + goModule
		v := m + "/@v/"
		file := func(name string) string { return goModule + "/@v/" + name }
		read := types.ActionRead
		return append([]matrixRow{
			row(goList, v+"list", "", listsGo("go-local", file("v1.0.0.mod")), 200),
			row(goListHead, v+"list", "", listsGo("go-local", file("v1.0.0.mod")), 200),
			// v1.0.0's .info is synthesised from the .mod, v1.1.0's stored.
			row(goInfo, v+"v1.0.0.info", "", holds("go-local", file("v1.0.0.mod"), read), 200),
			row(goInfo, v+"v1.1.0.info", "", holds("go-local", file("v1.1.0.info"), read), 200),
			row(goInfoHead, v+"v1.0.0.info", "", holds("go-local", file("v1.0.0.mod"), read), 200),
			row(goMod, v+"v1.0.0.mod", "", holds("go-local", file("v1.0.0.mod"), read), 200),
			row(goModHead, v+"v1.0.0.mod", "", holds("go-local", file("v1.0.0.mod"), read), 200),
			row(goZip, v+"v1.0.0.zip", "", holds("go-local", file("v1.0.0.zip"), read), 200),
			row(goZipHead, v+"v1.0.0.zip", "", holds("go-local", file("v1.0.0.zip"), read), 200),
			row(goLatest, m+"/@latest", "", listsGo("go-local", file("v1.1.0.mod")), 200),
			row(goLatestHead, m+"/@latest", "", listsGo("go-local", file("v1.1.0.mod")), 200),
			row(goLatestInfo, v+"latest.info", "", listsGo("go-local", file("v1.1.0.mod")), 200),
		}, goPublishMatrixRows()...)
	},
}

// goPublishMatrixRows: a deploy needs Write. The module path is outside
// the "work/" grants, since a module path's first element must hold a dot.
func goPublishMatrixRows() []matrixRow {
	const v = "example.com/m/@v/v1.0.0"
	return []matrixRow{
		row(goPut, goBase+"go-local/example.com/m-{p}/@v/v1.0.0.mod", "module example.com/m",
			holds("go-local", v+".mod", types.ActionWrite), 201),
		row(goPut, goBase+"go-local/example.com/m-{p}/@v/v1.0.0.info", `{"Version":"v1.0.0"}`,
			holds("go-local", v+".info", types.ActionWrite), 201),
	}
}

// listsGo: @v/list and @latest consider only the versions whose .mod the
// caller may read, so a caller who may read none finds no module, 404,
// rather than a refusal. Anonymous is still told to authenticate while
// anonymous access is off.
func listsGo(repoKey, mod string) rule {
	return func(p matrixPrincipal) int {
		switch {
		case p.anonymous && !p.anonymousOn:
			return http.StatusUnauthorized
		case p.holds(repoKey, mod, types.ActionRead):
			return 0
		}
		return http.StatusNotFound
	}
}
