package server

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// nugetRepos are NuGet's fixture repositories: nuget-local, nuget-remote
// and nuget-virtual, and nuget-forced, a local repository with
// forceNugetAuthentication on.
var nugetRepos = append(fixtureRepos("nuget", repo.NuGet), repo.Config{
	Key: "nuget-forced", Class: repo.Local, PackageType: repo.NuGet,
	ForceNugetAuthentication: &forceNuGet,
})

var forceNuGet = true

// NuGet's v3 routes (#543), by the paths decision 0038 lists. {hive} is
// registration or registration-semver2.
const (
	nugetV3           = "/artifactory/api/nuget/v3/"
	nugetIndex        = "GET " + nugetV3 + "{repoKey}/index.json"
	nugetIndexHead    = "HEAD " + nugetV3 + "{repoKey}/index.json"
	nugetFlatVersions = "GET " + nugetV3 + "{repoKey}/flatcontainer/{id}/index.json"
	nugetFlatFile     = "GET " + nugetV3 + "{repoKey}/flatcontainer/{id}/{version}/{file}"
	nugetFlatFileHead = "HEAD " + nugetV3 + "{repoKey}/flatcontainer/{id}/{version}/{file}"
	nugetRegIndex     = "GET " + nugetV3 + "{repoKey}/{hive}/{id}/index.json"
	nugetRegPage      = "GET " + nugetV3 + "{repoKey}/{hive}/{id}/page/{lower}/{upper}.json"
	nugetRegLeaf      = "GET " + nugetV3 + "{repoKey}/{hive}/{id}/{version}.json"
	nugetRegDownload  = "GET " + nugetV3 + "{repoKey}/{hive}/Download/{id}/{version}"
	// Push, delete, relist and the symbols push (#545).
	nugetV2      = "/artifactory/api/nuget/"
	nugetPush    = "PUT " + nugetV2 + "{repoKey}/{folder}"
	nugetDelete  = "DELETE " + nugetV2 + "{repoKey}/{id}/{version}"
	nugetRelist  = "POST " + nugetV2 + "{repoKey}/{id}/{version}"
	nugetSymbols = "PUT " + nugetV3 + "{repoKey}/symbols"
	// Search and autocomplete (#544).
	nugetSearch       = "GET " + nugetV3 + "{repoKey}/query"
	nugetAutocomplete = "GET " + nugetV3 + "{repoKey}/autocomplete"
)

// nugetStatusPart is NuGet's share of the status table, which its pull
// requests (#543 to #546) fill in. nugetreader reads nuget-local and may do
// nothing else there.
var nugetStatusPart = statusPart{
	handwritten: map[string]bool{
		nugetIndex: true, nugetIndexHead: true, nugetFlatVersions: true, nugetFlatFile: true,
		nugetFlatFileHead: true, nugetRegIndex: true, nugetRegPage: true, nugetRegLeaf: true,
		nugetRegDownload: true, nugetPush: true,
		nugetDelete: true, nugetRelist: true, nugetSymbols: true,
		nugetSearch: true, nugetAutocomplete: true,
		nugetSymbolRead: true, nugetSymbolHead: true,
	},
	rows: nugetStatusRows,
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		ctx := context.Background()
		items := &item.Service{Store: db, Blobs: blobs}
		seedNuGetSymbols(t, db, blobs)
		deployNuGet(t, db, items, "nuget-local", "Status.Pkg.1.0.0.nupkg",
			nugetPackage("Status.Pkg", "1.0.0", ""))
		deployNuGet(t, db, items, "nuget-local", "Status.Pkg.2.0.0-rc.1.nupkg",
			nugetPackage("Status.Pkg", "2.0.0-rc.1", ""))
		deployNuGet(t, db, items, "nuget-local", "Delete.Me.1.0.0.nupkg",
			nugetPackage("Delete.Me", "1.0.0", ""))
		deployNuGet(t, db, items, "nuget-local", "Delete.Me.1.0.0.snupkg",
			nugetSymbolsPackage("Delete.Me", "1.0.0"))
		if err := db.Users().Create(ctx, types.User{
			Name: "nugetreader", PasswordHash: cheapHash,
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: "nuget-read",
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"nugetreader": types.ActionRead,
				}},
				Targets: map[string]types.Target{"nuget-local": {Include: []string{"**"}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	},
}

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
	}, append(nugetWriteStatusRows(), nugetSymbolStatusRows()...)...)
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

// nugetForm is a row body that pushes pkg as NuGet does: a multipart form
// with one part named package, file name package.nupkg.
func nugetForm(pkg []byte) string {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.SetBoundary("artifactweave-nuget-boundary")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name=package; filename=package.nupkg`)
	h.Set("Content-Type", "application/octet-stream")
	w, _ := mw.CreatePart(h)
	_, _ = w.Write(pkg)
	_ = mw.Close()
	return "multipart:" + mw.Boundary() + "\n" + buf.String()
}

// nugetSymbolsPackage is the symbols package of id at version.
func nugetSymbolsPackage(id, version string) []byte {
	return nugetPackage(id, version,
		`<packageTypes><packageType name="SymbolsPackage" /></packageTypes>`)
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

// nugetPackage is a package of id at version, with more nuspec elements.
// Its entries are stored, not compressed, so the same package is always
// the same bytes, and a row can carry it in a string.
func nugetPackage(id, version, more string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct{ name, body string }{
		{"_rels/.rels", "<Relationships/>"},
		{id + ".nuspec", `<?xml version="1.0" encoding="utf-8"?>` +
			`<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">` +
			`<metadata><id>` + id + `</id><version>` + version + `</version>` +
			`<authors>Acme</authors><description>` + id + `</description>` + more +
			`</metadata></package>`},
		{"lib/net8.0/" + id + ".dll", "MZ"},
	} {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
		_, _ = io.WriteString(w, e.body)
	}
	_ = zw.Close()
	return buf.Bytes()
}

// The generated reindex operation remains authoritative at the v2 root.
// Exercise it on the full router, since the protocol-only fixture has no
// generated operation to reject unsupported methods.
func TestNuGetReindexMethod(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/api/nuget/nuget-local/reindex",
		basic("admin", "pw"),
		"",
	)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("reindex method: %d %s", rec.Code, rec.Body)
	}
}
