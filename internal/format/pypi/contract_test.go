package pypi_test

import (
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A version is its wheel and sdist at the canonical path, and a zip sdist
// deployed by REST to a folder of its own, which is indexed too, since a
// file is indexed wherever it is stored. "alpha" is named with upper case
// and a dot, so its project and folder are its normalised name. A README
// beside the wheel is stored but no file of the version. Yanking is the
// property the index follows.
func TestContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.PyPI,
		Version: func(p, v string, n int) formattest.Version {
			name := map[string]string{"alpha": "Formattest.Alpha", "beta": "beta"}[p]
			m := meta(name, v, fmt.Sprintf("Summary: deploy %d", n), "Requires-Python: >=3.9")
			folder := pypi.Normalize(name) + "/" + v + "/"
			whl, tgz := folder+wheelName(name, v), folder+distName(name)+"-"+v+".tar.gz"
			zipped := "uploads/" + distName(name) + "-" + v + ".zip"
			return formattest.Version{
				Package: pypi.Normalize(name), Version: v,
				Files: []formattest.File{
					{Path: whl, Body: wheel(name, v, m)},
					{Path: tgz, Body: sdist(name, v, m)},
					{Path: zipped, Body: zipOf(entry{"PKG-INFO", m})},
					{Path: folder + "README.txt", Body: []byte("read me")},
				},
				Linked: []string{whl, tgz, zipped},
			}
		},
		Properties: []types.Properties{
			{pypi.PropYanked: {"true"}},
			{pypi.PropYanked: {"use 2.0"}, "formattest.k": {"x"}},
		},
	})
}
