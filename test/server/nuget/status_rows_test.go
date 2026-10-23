package nuget_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const (
	nugetV2           = "/artifactory/api/nuget/"
	nugetIndex        = "GET " + nugetV3 + "{repoKey}/index.json"
	nugetIndexHead    = "HEAD " + nugetV3 + "{repoKey}/index.json"
	nugetFlatVersions = "GET " + nugetV3 + "{repoKey}/flatcontainer/{id}/index.json"
	nugetFlatFile     = "GET " + nugetV3 + "{repoKey}/flatcontainer/{id}/{version}/{file}"
	nugetFlatFileHead = "HEAD " + nugetV3 + "{repoKey}/flatcontainer/{id}/{version}/{file}"
	nugetRegIndex     = "GET " + nugetV3 + "{repoKey}/{hive}/{id}/index.json"
	nugetRegPage      = "GET " + nugetV3 + "{repoKey}/{hive}/{id}/page/{lower}/{upper}.json"
	nugetRegLeaf      = "GET " + nugetV3 + "{repoKey}/{hive}/{id}/{version}.json"
	nugetRegDownload  = "GET " + nugetV3 + "{repoKey}/{hive}/Download/{id}/{version}"
	nugetPush         = "PUT " + nugetV2 + "{repoKey}/{folder}"
	nugetDelete       = "DELETE " + nugetV2 + "{repoKey}/{id}/{version}"
	nugetRelist       = "POST " + nugetV2 + "{repoKey}/{id}/{version}"
	nugetSymbols      = "PUT " + nugetV3 + "{repoKey}/symbols"
	nugetSearch       = "GET " + nugetV3 + "{repoKey}/query"
	nugetAutocomplete = "GET " + nugetV3 + "{repoKey}/autocomplete"
	nugetSymbolRead   = "GET " + nugetV3 + "{repoKey}/{file}.pdb/{key}/{file}.pdb"
	nugetSymbolHead   = "HEAD " + nugetV3 + "{repoKey}/{file}.pdb/{key}/{file}.pdb"
)

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

type statusCase struct {
	route, path, as, body string
	want                  int
}

func (c statusCase) method() string { m, _, _ := strings.Cut(c.route, " "); return m }

func nugetStatusRows() []statusCase {
	local := nugetV3 + "nuget-local/"
	flat := local + "flatcontainer/status.pkg/"
	nupkg := flat + "1.0.0/status.pkg.1.0.0.nupkg"
	reg := local + "registration/status.pkg/"
	semver2 := local + "registration-semver2/status.pkg/"
	download := local + "registration/Download/Status.Pkg/1.0.0"
	return append([]statusCase{
		// The service index answers whoever may reach the repository.
		{nugetIndex, local + "index.json", "admin", "", 200},
		{nugetIndex, local + "index.json", "user", "", 200},
		{nugetIndex, local + "index.json", "anonymous", "", 200},
		{nugetIndex, local + "index.json", "", "", 401},
		{nugetIndex, nugetV3 + "nuget-local", "admin", "", 200},
		{nugetIndex, local, "admin", "", 200},
		{nugetIndex, nugetV3 + "missing/index.json", "admin", "", 404},
		{nugetIndex, nugetV3 + "libs-local/index.json", "admin", "", 404},
		// Remote and virtual NuGet repositories are P9's and P10's.
		{nugetIndex, nugetV3 + "nuget-remote/index.json", "admin", "", 501},
		{nugetIndex, nugetV3 + "nuget-virtual/index.json", "admin", "", 501},
		{nugetIndex, local + "FindPackagesById()", "admin", "", 404},
		{nugetIndexHead, local + "index.json", "admin", "", 200},
		{nugetIndexHead, local + "index.json", "", "", 401},

		// A package the caller may read no version of reads as none.
		{nugetFlatVersions, flat + "index.json", "admin", "", 200},
		{nugetFlatVersions, local + "flatcontainer/STATUS.PKG/index.json", "admin", "", 200},
		{nugetFlatVersions, flat + "index.json", "user", "", 404},
		{nugetFlatVersions, flat + "index.json", "anonymous", "", 404},
		{nugetFlatVersions, flat + "index.json", "", "", 401},
		{nugetFlatVersions, local + "flatcontainer/absent/index.json", "admin", "", 404},
		{nugetFlatFile, nupkg, "admin", "", 200},
		{nugetFlatFile, flat + "1.0.0/status.pkg.nuspec", "admin", "", 200},
		{nugetFlatFile, flat + "1.0/status.pkg.1.0.nupkg", "admin", "", 200},
		{nugetFlatFile, flat + "9.0.0/status.pkg.9.0.0.nupkg", "admin", "", 404},
		// A download is refused as the item routes refuse one.
		{nugetFlatFile, nupkg, "user", "", 403},
		{nugetFlatFile, nupkg, "anonymous", "", 401},
		{nugetFlatFile, nupkg, "", "", 401},
		{nugetFlatFileHead, nupkg, "admin", "", 200},
		{nugetFlatFileHead, nupkg, "user", "", 403},

		{nugetRegIndex, reg + "index.json", "admin", "", 200},
		{nugetRegIndex, semver2 + "index.json", "admin", "", 200},
		{nugetRegIndex, reg + "index.json", "user", "", 404},
		{nugetRegIndex, reg + "index.json", "anonymous", "", 404},
		{nugetRegIndex, reg + "index.json", "", "", 401},
		{nugetRegIndex, local + "registration/absent/index.json", "admin", "", 404},
		{nugetRegPage, reg + "page/1.0.0/1.0.0.json", "admin", "", 200},
		{nugetRegPage, reg + "page/0.1.0/9.0.0.json", "admin", "", 200},
		// 2.0.0-rc.1's label has two parts, which makes it SemVer 2.0.0: only
		// the 3.6.0 hive shows it.
		{nugetRegPage, reg + "page/2.0.0-rc.1/2.0.0-rc.1.json", "admin", "", 404},
		{nugetRegPage, semver2 + "page/2.0.0-rc.1/2.0.0-rc.1.json", "admin", "", 200},
		{nugetRegPage, reg + "page/3.0.0/4.0.0.json", "admin", "", 404},
		{nugetRegPage, reg + "page/x/y.json", "admin", "", 404},
		{nugetRegPage, reg + "page/1.0.0/1.0.0.json", "user", "", 404},
		{nugetRegLeaf, reg + "1.0.0.json", "admin", "", 200},
		{nugetRegLeaf, semver2 + "2.0.0-rc.1.json", "admin", "", 200},
		{nugetRegLeaf, reg + "9.0.0.json", "admin", "", 404},
		{nugetRegLeaf, reg + "1.0.0.json", "user", "", 404},
		{nugetRegLeaf, reg + "1.0.0.json", "", "", 401},
		{nugetRegDownload, download, "admin", "", 200},
		{
			nugetRegDownload,
			local + "registration-semver2/Download/Status.Pkg/2.0.0-rc.1",
			"admin",
			"",
			200,
		},
		{nugetRegDownload, local + "registration/Download/Status.Pkg/9.0.0", "admin", "", 404},
		{nugetRegDownload, download, "user", "", 403},
		{nugetRegDownload, download, "anonymous", "", 401},
		// A search lists what its caller may read, which may be nothing.
		{nugetSearch, local + "query?q=status&prerelease=true&semVerLevel=2.0.0", "admin", "", 200},
		{nugetSearch, local + "query", "user", "", 200},
		{nugetSearch, local + "query", "anonymous", "", 200},
		{nugetSearch, local + "query", "", "", 401},
		{nugetSearch, local + "query?skip=x&take=-1", "admin", "", 200},
		{nugetSearch, nugetV3 + "missing/query", "admin", "", 404},
		{nugetSearch, nugetV3 + "nuget-remote/query", "admin", "", 501},
		{nugetAutocomplete, local + "autocomplete?q=status", "admin", "", 200},
		{nugetAutocomplete, local + "autocomplete?id=status.pkg", "admin", "", 200},
		{nugetAutocomplete, local + "autocomplete?q=status", "user", "", 200},
		{nugetAutocomplete, local + "autocomplete?q=status", "anonymous", "", 200},
		{nugetAutocomplete, local + "autocomplete", "", "", 401},
		{nugetAutocomplete, nugetV3 + "libs-local/autocomplete", "admin", "", 404},
	}, append(append(nugetWriteStatusRows(), nugetV2StatusRows()...), nugetSymbolStatusRows()...)...)
}

// nugetWriteStatusRows are the push's, the delete's, the relist's and the
// symbols push's (#545), and force authentication's.
func nugetWriteStatusRows() []statusCase {
	v2 := nugetV2 + "nuget-local/"
	symbols := nugetV3 + "nuget-local/symbols"
	push := func(id, version string) string {
		return nugetForm(nugetPackage(id, version, ""))
	}
	forced := nugetV3 + "nuget-forced/"
	return []statusCase{
		// 201 with no body, at the root or in the folder named.
		{nugetPush, v2, "admin", push("Push.Root", "1.0.0"), 201},
		{nugetPush, nugetV2 + "nuget-local", "admin", push("Push.Bare", "1.0.0"), 201},
		{nugetPush, v2 + "team/sub/", "admin", push("Push.Folder", "1.0.0"), 201},
		// A version is held once, for every caller: what --skip-duplicate
		// accepts.
		{nugetPush, v2, "admin", push("Status.Pkg", "1.0.0"), 409},
		{nugetPush, v2 + "other/", "admin", push("Status.Pkg", "1.0"), 409},
		{nugetPush, v2, "admin", nugetForm([]byte("not a package")), 400},
		{nugetPush, v2, "admin", nugetForm(nugetSymbolsPackage("Push.Sym", "1.0.0")), 400},
		{nugetPush, v2, "admin", "{}", 415},
		{nugetPush, v2, "nugetreader:pw", push("Push.Denied", "1.0.0"), 403},
		{nugetPush, v2, "user", push("Push.Denied", "1.0.0"), 403},
		{nugetPush, v2, "anonymous", push("Push.Denied", "1.0.0"), 401},
		{nugetPush, v2, "", push("Push.Denied", "1.0.0"), 401},
		{nugetPush, nugetV2 + "missing/", "admin", push("Push.Denied", "1.0.0"), 404},
		{nugetPush, nugetV2 + "libs-local/", "admin", push("Push.Denied", "1.0.0"), 404},
		{nugetPush, nugetV2 + "nuget-remote/", "admin", push("Push.Denied", "1.0.0"), 501},
		{nugetPush, nugetV2 + "nuget-forced/", "admin", push("Push.Forced", "1.0.0"), 201},
		{nugetPush, nugetV2 + "nuget-forced/", "anonymous", push("Push.Denied", "1.0.0"), 401},

		{nugetSymbols, symbols, "admin", nugetForm(nugetSymbolsPackage("Sym.Alone", "1.0.0")), 201},
		{
			nugetSymbols,
			symbols + "/",
			"admin",
			nugetForm(nugetSymbolsPackage("Status.Pkg", "1.0.0")),
			201,
		},
		{nugetSymbols, symbols, "admin", nugetForm(nugetSymbolsPackage("Delete.Me", "1.0.0")), 409},
		{nugetSymbols, symbols, "admin", push("Sym.Plain", "1.0.0"), 400},
		{nugetSymbols, symbols, "user", nugetForm(nugetSymbolsPackage("Sym.Denied", "1.0.0")), 403},
		{
			nugetSymbols,
			symbols,
			"anonymous",
			nugetForm(nugetSymbolsPackage("Sym.Denied", "1.0.0")),
			401,
		},

		// A hard delete, 204; a version the caller may not read is not
		// revealed.
		{nugetDelete, v2 + "Delete.Me/1.0.0", "admin", "", 204},
		{nugetDelete, v2 + "Status.Pkg/9.9.9", "admin", "", 404},
		{nugetDelete, v2 + "team/Status.Pkg/1.0.0", "admin", "", 404},
		{nugetDelete, v2 + "Status.Pkg/1.0.0", "nugetreader:pw", "", 403},
		{nugetDelete, v2 + "Status.Pkg/1.0.0", "user", "", 404},
		{nugetDelete, v2 + "Status.Pkg/1.0.0", "anonymous", "", 404},
		{nugetDelete, v2 + "Status.Pkg/1.0.0", "", "", 401},
		{nugetDelete, nugetV2 + "nuget-forced/Status.Pkg/1.0.0", "anonymous", "", 401},
		// No unlisted state: a relist changes nothing.
		{nugetRelist, v2 + "Status.Pkg/1.0.0", "admin", "", 200},
		{nugetRelist, v2 + "status.pkg/1.0", "admin", "", 200},
		{nugetRelist, v2 + "Status.Pkg/1.0.0", "nugetreader:pw", "", 403},
		{nugetRelist, v2 + "Status.Pkg/1.0.0", "user", "", 404},
		{nugetRelist, v2 + "Status.Pkg/9.9.9", "admin", "", 404},
		{nugetRelist, v2 + "Status.Pkg/1.0.0", "", "", 401},

		// Force authentication challenges anonymous before routing, and
		// lets credentials through.
		{nugetIndex, forced + "index.json", "anonymous", "", 401},
		{nugetIndex, forced + "index.json", "user", "", 200},
		{nugetIndex, forced + "index.json", "admin", "", 200},
		{nugetFlatVersions, forced + "flatcontainer/absent/index.json", "anonymous", "", 401},
		{nugetFlatVersions, forced + "flatcontainer/absent/index.json", "admin", "", 404},
	}
}

// deployNuGet deploys body at path in the repository key through items,
// so the NuGet format checks and indexes it.
func deployNuGet(t *testing.T, db store.Store, items *item.Service, key, path string, body []byte) {
	t.Helper()
	ctx := context.Background()
	c, err := db.Repos().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: bytes.NewReader(body),
	}); err != nil {
		t.Fatal(err)
	}
}

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

func nugetSymbolStatusRows() []statusCase {
	p := nugetV3 + "nuget-local/app.pdb/" + nugetPDBKey + "/app.pdb"
	forced := nugetV3 + "nuget-forced/app.pdb/" + nugetPDBKey + "/app.pdb"
	return []statusCase{
		{nugetSymbolRead, p, "admin", "", 200},
		{nugetSymbolRead, strings.ToLower(p), "nugetreader:pw", "", 200},
		{nugetSymbolHead, p, "admin", "", 200},
		{nugetSymbolRead, p, "user", "", 403},
		{nugetSymbolRead, p, "anonymous", "", 401},
		{nugetSymbolRead, p, "", "", 401},
		{nugetSymbolRead, strings.Replace(p, nugetPDBKey, nugetPDBGUID+"1", 1), "admin", "", 404},
		{nugetSymbolRead, strings.Replace(p, nugetPDBKey, "bad", 1), "admin", "", 404},
		{nugetSymbolRead, strings.Replace(p, "nuget-local", "missing", 1), "admin", "", 404},
		{nugetSymbolRead, strings.Replace(p, "nuget-local", "libs-local", 1), "admin", "", 404},
		{nugetSymbolRead, strings.Replace(p, "nuget-local", "nuget-remote", 1), "admin", "", 501},
		{nugetSymbolRead, strings.Replace(p, "nuget-local", "nuget-virtual", 1), "admin", "", 501},
		{nugetSymbolRead, forced, "anonymous", "", 401},
		{nugetSymbolRead, forced, "admin", "", 200},
		{nugetSymbolHead, p, "user", "", 403},
		{nugetSymbolHead, forced, "anonymous", "", 401},
	}
}
