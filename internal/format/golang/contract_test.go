package golang_test

import (
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// A module is a package and each version a version linking its .zip, .mod
// and .info, deployed in jf go-publish's order. The module path has an
// upper-case element, stored as written, and version "2.0" is an
// +incompatible one. Redeploying a version changes only its .info: a
// different .zip or .mod is refused (TestUnitRepublish).
func TestUnitContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.Go,
		Version: func(p, v string, n int) formattest.Version {
			m := "github.com/Example/" + p
			ver := map[string]string{"1.0": "v1.0.0", "2.0": "v2.0.0+incompatible"}[v]
			at := m + "/@v/" + ver
			return formattest.Version{
				Package: m, Version: ver,
				Files: []formattest.File{
					{Path: at + ".zip", Body: []byte("zip of " + m + "@" + ver)},
					{Path: at + ".mod", Body: []byte("module " + m + "\n")},
					{
						Path: at + ".info",
						Body: fmt.Appendf(
							nil,
							`{"Version":%q,"Time":"2026-10-%02dT09:00:00Z"}`,
							ver,
							n+1,
						),
					},
				},
			}
		},
	})
}
