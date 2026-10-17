package server

// npmMatrixPart is npm's share of the route × principal matrix, which
// its pull requests (#286–#294) fill in.
var npmMatrixPart = matrixPart{
	rows: func() []matrixRow {
		return []matrixRow{
			row(npmReindex, "/artifactory/api/npm/npm-local/reindex", "", administrators, 200),
		}
	},
}
