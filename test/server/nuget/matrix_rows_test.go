package nuget_test

import (
	"net/http"
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
		f.deploy(
			t,
			"nuget-local",
			"Debug.1.0.0.snupkg",
			nugetPDBPackage("Debug", nugetPortablePDB()),
		)
		f.deploy(
			t,
			"nuget-forced",
			"Debug.1.0.0.snupkg",
			nugetPDBPackage("Debug", nugetPortablePDB()),
		)
		freshNuGetSymbols(t, f.items)
	},
	rows: func() []matrixRow {
		local := nugetV3 + "nuget-local/"
		read := types.ActionRead
		return append([]matrixRow{
			row(nugetIndex, local+"index.json", "", anyone, 200),
			row(
				nugetSymbolRead,
				local+"app.pdb/"+nugetPDBKey+"/app.pdb",
				"",
				holds("nuget-local", nugetSymbolPath, read),
				200,
			),
			row(
				nugetSymbolHead,
				local+"app.pdb/"+nugetPDBKey+"/app.pdb",
				"",
				holds("nuget-local", nugetSymbolPath, read),
				200,
			),
			row(
				nugetSymbolRead,
				nugetV3+"nuget-forced/app.pdb/"+nugetPDBKey+"/app.pdb",
				"",
				challenged(holds("nuget-forced", nugetSymbolPath, read)),
				200,
			),
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
			// Each lists what its caller may read.
			row(nugetSearch, local+"query?q=lib", "", anyone, 200),
			row(nugetAutocomplete, local+"autocomplete?q=w", "", anyone, 200),
			row(nugetAutocomplete, local+"autocomplete?id=work", "", anyone, 200),
		}, append(nugetWriteMatrixRows(), nugetV2MatrixRows()...)...)
	},
}

// nugetWriteMatrixRows are the push's, the delete's, the relist's and the
// symbols push's (#545), and force authentication's.
func nugetWriteMatrixRows() []matrixRow {
	v2 := nugetV2 + "nuget-local/"
	write, del := types.ActionWrite, types.ActionDelete
	forced := nugetV3 + "nuget-forced/"
	return []matrixRow{
		// Deploy in work/, then at the canonical path there. Each principal
		// pushes to a fixture of its own: a version is pushed once.
		row(nugetPush, v2+"work/", nugetForm(nugetPackage("Work", "2.0.0", "")),
			holds("nuget-local", "work/Work.2.0.0.nupkg", write), 201).alone(),
		// A version held already answers 409 to whoever may deploy it.
		row(nugetPush, v2+"work/", nugetForm(nugetPackage("Work", "1.0.0", "")),
			holds("nuget-local", nugetWork, write), 409),
		// Symbols land at the root.
		row(nugetSymbols, nugetV3+"nuget-local/symbols",
			nugetForm(nugetSymbolsPackage("Lib", "1.0.0")),
			holds("nuget-local", "Lib.1.0.0.snupkg", write), 201).alone(),
		// Each principal deletes a version of its own, in work/.
		row(nugetDelete, v2+"Del-{p}/1.0.0", "", nugetActs("work/Del.1.0.0.nupkg", del), 204).
			seeding(func(t *testing.T, f *matrixFixture, p string) {
				t.Helper()
				f.deploy(t, "nuget-local", "work/Del-"+p+".1.0.0.nupkg",
					nugetPackage("Del-"+p, "1.0.0", ""))
			}),
		row(nugetRelist, v2+"Work/1.0.0", "", nugetActs(nugetWork, types.ActionAnnotate), 200),
		// Force authentication: anonymous is challenged whatever it may
		// read, and anyone else answered as without it.
		row(nugetIndex, forced+"index.json", "", challenged(anyone), 200),
		row(nugetPush, nugetV2+"nuget-forced/work/", nugetForm(nugetPackage("Work", "3.0.0", "")),
			challenged(holds("nuget-forced", "work/Work.3.0.0.nupkg", write)), 201).alone(),
	}
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

// nugetActs: a delete or a relist finds the version only for a caller who
// may read its .nupkg at path, 404 to any other, so a version the caller
// may not read is not revealed; it then needs want there.
func nugetActs(path string, want types.Actions) rule {
	return func(p matrixPrincipal) int {
		if s := listsGo("nuget-local", path)(p); s != 0 {
			return s
		}
		return holds("nuget-local", path, want)(p)
	}
}

// challenged is r on a repository with forceNugetAuthentication on, which
// answers anonymous 401 whatever it may read.
func challenged(r rule) rule {
	return func(p matrixPrincipal) int {
		if p.anonymous {
			return http.StatusUnauthorized
		}
		return r(p)
	}
}

func nugetV2MatrixRows() []matrixRow {
	local := nugetV2 + "nuget-local/"
	var rows []matrixRow
	for _, route := range []struct {
		get, head, path string
		allow           rule
	}{
		{nugetV2Service, nugetV2ServiceHead, "", anyone},
		{nugetV2Metadata, nugetV2MetadataHead, "$metadata", anyone},
		{nugetV2Entry, nugetV2EntryHead, "Packages(Id='Work',Version='1.0')", nugetLists(nugetWork)},
		{nugetV2Packages, nugetV2PackagesHead, "Packages()", anyone},
		{nugetV2Find, nugetV2FindHead, "FindPackagesById()?id='Work'", anyone},
		{nugetV2Search, nugetV2SearchHead, "Search()?searchTerm='work'", anyone},
		{nugetV2Count, nugetV2CountHead, "Packages()/$count", anyone},
		{nugetV2Download, nugetV2DownloadHead, "Download/Work/1.0", holds("nuget-local", nugetWork, types.ActionRead)},
	} {
		for _, operation := range []string{route.get, route.head} {
			rows = append(rows, row(operation, local+route.path, "", route.allow, 200))
		}
	}
	return append(rows, row(nugetV2Service, nugetV2+"nuget-forced/", "", challenged(anyone), 200))
}
