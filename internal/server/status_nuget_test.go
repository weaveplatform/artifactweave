package server

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// nugetRepos are NuGet's fixture repositories: nuget-local, nuget-remote
// and nuget-virtual.
var nugetRepos = fixtureRepos("nuget", repo.NuGet)

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
)

// nugetStatusPart is NuGet's share of the status table, which its pull
// requests (#543 to #546) fill in.
var nugetStatusPart = statusPart{
	handwritten: map[string]bool{
		nugetIndex: true, nugetIndexHead: true, nugetFlatVersions: true, nugetFlatFile: true,
		nugetFlatFileHead: true, nugetRegIndex: true, nugetRegPage: true, nugetRegLeaf: true,
		nugetRegDownload: true,
	},
	rows: nugetStatusRows,
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		items := &item.Service{Store: db, Blobs: blobs}
		deployNuGet(t, db, items, "nuget-local", "Status.Pkg.1.0.0.nupkg",
			nugetPackage("Status.Pkg", "1.0.0", ""))
		deployNuGet(t, db, items, "nuget-local", "Status.Pkg.2.0.0-rc.1.nupkg",
			nugetPackage("Status.Pkg", "2.0.0-rc.1", ""))
	},
}

func nugetStatusRows() []statusCase {
	local := nugetV3 + "nuget-local/"
	flat := local + "flatcontainer/status.pkg/"
	nupkg := flat + "1.0.0/status.pkg.1.0.0.nupkg"
	reg := local + "registration/status.pkg/"
	semver2 := local + "registration-semver2/status.pkg/"
	download := local + "registration/Download/Status.Pkg/1.0.0"
	return []statusCase{
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
