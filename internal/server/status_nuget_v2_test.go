package server

import "github.com/weaveplatform/artifactweave/pkg/types"

const (
	nugetV2Service      = "GET " + nugetV2 + "{repoKey}/"
	nugetV2ServiceHead  = "HEAD " + nugetV2 + "{repoKey}/"
	nugetV2Metadata     = "GET " + nugetV2 + "{repoKey}/$metadata"
	nugetV2MetadataHead = "HEAD " + nugetV2 + "{repoKey}/$metadata"
	nugetV2Entry        = "GET " + nugetV2 + "{repoKey}/Packages(Id='{id}',Version='{version}')"
	nugetV2EntryHead    = "HEAD " + nugetV2 + "{repoKey}/Packages(Id='{id}',Version='{version}')"
	nugetV2Packages     = "GET " + nugetV2 + "{repoKey}/Packages()"
	nugetV2PackagesHead = "HEAD " + nugetV2 + "{repoKey}/Packages()"
	nugetV2Find         = "GET " + nugetV2 + "{repoKey}/FindPackagesById()"
	nugetV2FindHead     = "HEAD " + nugetV2 + "{repoKey}/FindPackagesById()"
	nugetV2Search       = "GET " + nugetV2 + "{repoKey}/Search()"
	nugetV2SearchHead   = "HEAD " + nugetV2 + "{repoKey}/Search()"
	nugetV2Count        = "GET " + nugetV2 + "{repoKey}/{query}/$count"
	nugetV2CountHead    = "HEAD " + nugetV2 + "{repoKey}/{query}/$count"
	nugetV2Download     = "GET " + nugetV2 + "{repoKey}/Download/{id}/{version}"
	nugetV2DownloadHead = "HEAD " + nugetV2 + "{repoKey}/Download/{id}/{version}"
)

func nugetV2StatusRows() []statusCase {
	local := nugetV2 + "nuget-local/"
	rows := []statusCase{
		{nugetV2Service, nugetV2 + "nuget-local", "admin", "", 200},
		{nugetV2Service, nugetV2 + "missing/", "admin", "", 404},
		{nugetV2Service, nugetV2 + "libs-local/", "admin", "", 404},
		{nugetV2Service, nugetV2 + "nuget-remote/", "admin", "", 501},
		{nugetV2Service, nugetV2 + "nuget-virtual/", "admin", "", 501},
		{nugetV2Service, nugetV2 + "nuget-forced/", "anonymous", "", 401},
		{nugetV2Metadata, nugetV2 + "nuget-forced/$metadata", "anonymous", "", 401},
		{nugetV2Entry, local + "Packages(Id='Status.Pkg',Version='1.0')", "user", "", 404},
		{nugetV2Entry, local + "Packages(Id='Status.Pkg',Version='9.0')", "admin", "", 404},
		{
			nugetV2Entry,
			local + "Packages(Id='STATUS.PKG',Version='1.0.0%2Bbuild')",
			"admin",
			"",
			200,
		},
		{nugetV2Find, local + "FindPackagesById()?id='absent'", "admin", "", 200},
		{nugetV2Find, local + "FindPackagesById()?id='Status.Pkg'", "user", "", 200},
		{nugetV2Packages, local + "Packages", "admin", "", 200},
		{nugetV2Packages, local + "Packages()?$select=Id", "admin", "", 400},
		{nugetV2Packages, local + "Packages()?$filter=Id%20eq%20'x'", "admin", "", 400},
		{nugetV2Packages, local + "GetUpdates()", "admin", "", 404},
		{nugetV2Count, local + "GetUpdates()/$count", "admin", "", 404},
		{nugetV2Count, local + "FindPackagesById()/$count?id='Status.Pkg'", "admin", "", 200},
		{nugetV2Count, local + "Search()/$count?searchTerm='Status'", "admin", "", 200},
		{nugetV2Download, local + "Download/Status.Pkg/1.0", "user", "", 403},
		{nugetV2Download, local + "Download/Status.Pkg/1.0", "anonymous", "", 401},
		{nugetV2Download, local + "package/Status.Pkg/1.0", "admin", "", 404},
	}
	for _, route := range []struct{ get, head, path string }{
		{nugetV2Service, nugetV2ServiceHead, ""},
		{nugetV2Metadata, nugetV2MetadataHead, "$metadata"},
		{nugetV2Entry, nugetV2EntryHead, "Packages(Id='Status.Pkg',Version='1.0')"},
		{nugetV2Packages, nugetV2PackagesHead, "Packages()"},
		{nugetV2Find, nugetV2FindHead, "FindPackagesById()?id='Status.Pkg'"},
		{nugetV2Search, nugetV2SearchHead, "Search()?searchTerm='Status'"},
		{nugetV2Count, nugetV2CountHead, "Packages()/$count"},
		{nugetV2Download, nugetV2DownloadHead, "Download/Status.Pkg/1.0"},
	} {
		for _, operation := range []string{route.get, route.head} {
			rows = append(
				rows,
				statusCase{operation, local + route.path, "admin", "", 200},
				statusCase{operation, local + route.path, "", "", 401},
			)
		}
	}
	return rows
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
