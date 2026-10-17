package server

import "github.com/weaveplatform/artifactweave/pkg/types"

// pkgtestMatrixPart is the test-only protocol's share of the matrix: the
// kit's permission checks agree with the permission model as the item
// routes' do.
var pkgtestMatrixPart = matrixPart{
	rows: func() []matrixRow {
		file := pkgtestBase + "libs-local/files/work/get.txt"
		return []matrixRow{
			row(pkgtestGet, file, "", holds("libs-local", "work/get.txt", types.ActionRead), 200),
			row(pkgtestHead, file, "", holds("libs-local", "work/get.txt", types.ActionRead), 200),
			row(pkgtestPut, pkgtestBase+"libs-local/files/work/pkgtest-{p}.txt", "pkgtest",
				holds("libs-local", "work/pkgtest.txt", types.ActionWrite), 201),
		}
	},
	tune: func(c *Config) { c.Protocols = append(c.Protocols, pkgtestProtocol()) },
}
