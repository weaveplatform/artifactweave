package nuget_test

import (
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// A version is its .nupkg and its .snupkg at their canonical names, each
// with the id's case and the normalised version: the suite's 1.0 is
// written 1.0 in the nuspec and stored as 1.0.0, and its 2.0 is a SemVer 2
// pre-release with build metadata, which the name drops and the index
// lower-cases. "beta" lives in a folder, as a push to one stores it. Each
// version also has a legacy .symbols.nupkg, which is stored but never
// indexed.
func TestContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.NuGet,
		Version: func(p, v string, n int) formattest.Version {
			id, dir := "Alpha.Lib", ""
			if p == "beta" {
				id, dir = "Beta", "team/"
			}
			written, stored, key := "1.0", "1.0.0", "1.0.0"
			if v == "2.0" {
				written, stored, key = "2.0.0-RC.1+build.7", "2.0.0-RC.1", "2.0.0-rc.1"
			}
			base := dir + id + "." + stored
			body := pkg(id, written, fmt.Sprintf("<description>deploy %d</description>", n))
			return formattest.Version{
				Package: nameOf(id), Version: key,
				Files: []formattest.File{
					{Path: base + ".nupkg", Body: body},
					{Path: base + ".snupkg", Body: symbols(id, written)},
					{Path: base + ".symbols.nupkg", Body: body},
				},
				Linked: []string{base + ".nupkg", base + ".snupkg"},
			}
		},
	})
}

func nameOf(id string) string {
	return map[string]string{"Alpha.Lib": "alpha.lib", "Beta": "beta"}[id]
}
