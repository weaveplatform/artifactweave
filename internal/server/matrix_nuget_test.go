package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// nugetMatrixPart is NuGet's share of the route × principal matrix, which
// its pull requests (#543 to #546) fill in. Work's package lies under the
// writer's grants, in the work/ folder a push names, and Lib's at the
// repository root, outside them.
var nugetMatrixPart = matrixPart{
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		f.deploy(t, "nuget-local", nugetWork, nugetPackage("Work", "1.0.0", ""))
		f.deploy(t, "nuget-local", nugetLib, nugetPackage("Lib", "1.0.0", ""))
	},
	rows: func() []matrixRow {
		local := nugetV3 + "nuget-local/"
		read := types.ActionRead
		return []matrixRow{
			row(nugetIndex, local+"index.json", "", anyone, 200),
			row(nugetIndexHead, local+"index.json", "", anyone, 200),
			row(nugetFlatVersions, local+"flatcontainer/work/index.json", "",
				nugetLists(nugetWork), 200),
			row(nugetFlatVersions, local+"flatcontainer/lib/index.json", "",
				nugetLists(nugetLib), 200),
			row(nugetFlatFile, local+"flatcontainer/work/1.0.0/work.1.0.0.nupkg", "",
				holds("nuget-local", nugetWork, read), 200),
			row(nugetFlatFile, local+"flatcontainer/lib/1.0.0/lib.nuspec", "",
				holds("nuget-local", nugetLib, read), 200),
			row(nugetFlatFileHead, local+"flatcontainer/lib/1.0.0/lib.1.0.0.nupkg", "",
				holds("nuget-local", nugetLib, read), 200),
			row(nugetRegIndex, local+"registration/work/index.json", "",
				nugetLists(nugetWork), 200),
			row(nugetRegIndex, local+"registration-semver2/lib/index.json", "",
				nugetLists(nugetLib), 200),
			row(nugetRegPage, local+"registration/lib/page/1.0.0/1.0.0.json", "",
				nugetLists(nugetLib), 200),
			row(nugetRegLeaf, local+"registration/work/1.0.0.json", "",
				nugetLists(nugetWork), 200),
			row(nugetRegDownload, local+"registration/Download/Lib/1.0.0", "",
				holds("nuget-local", nugetLib, read), 200),
		}
	},
}

// The matrix's packages: Work in the work/ folder, Lib at the root.
const (
	nugetWork = "work/Work.1.0.0.nupkg"
	nugetLib  = "Lib.1.0.0.nupkg"
)

// nugetLists: the feeds list only the versions whose .nupkg the caller may
// read, so a caller who may read none finds no package, 404, rather than a
// refusal, as Go's version lists do (listsGo).
func nugetLists(path string) rule { return listsGo("nuget-local", path) }
