package server

import (
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// goRepos are Go's fixture repositories: go-local, go-remote and
// go-virtual.
var goRepos = fixtureRepos("go", repo.Go)

// The GOPROXY reads (#273), by the paths decision 0035 lists.
const (
	goBase       = "/artifactory/api/go/"
	goList       = "GET " + goBase + "{repoKey}/{module}/@v/list"
	goListHead   = "HEAD " + goBase + "{repoKey}/{module}/@v/list"
	goInfo       = "GET " + goBase + "{repoKey}/{module}/@v/{version}.info"
	goInfoHead   = "HEAD " + goBase + "{repoKey}/{module}/@v/{version}.info"
	goMod        = "GET " + goBase + "{repoKey}/{module}/@v/{version}.mod"
	goModHead    = "HEAD " + goBase + "{repoKey}/{module}/@v/{version}.mod"
	goZip        = "GET " + goBase + "{repoKey}/{module}/@v/{version}.zip"
	goZipHead    = "HEAD " + goBase + "{repoKey}/{module}/@v/{version}.zip"
	goLatest     = "GET " + goBase + "{repoKey}/{module}/@latest"
	goLatestHead = "HEAD " + goBase + "{repoKey}/{module}/@latest"
	goLatestInfo = "GET " + goBase + "{repoKey}/{module}/@v/latest.info"
	// goSumDB is the checksum database's 404, answered before
	// authentication. It is no route the guards hold to the matrix, which
	// answers credentials that do not verify 401 everywhere.
	goSumDB = "GET " + goBase + "{repoKey}/sumdb/*"
)

// goModule is the module the fixtures seed in go-local: v1.0.0 with its
// .zip and .mod, v1.1.0 with a stored .info as well, and v1.2.0 with its
// .zip alone, which Go reads do not see yet.
const goModule = "example.com/acme/lib"

// seedGoModule deploys goModule's files to go-local through the item
// service, which indexes them as the Go format does.
func seedGoModule(t *testing.T, items *item.Service, db store.Store) {
	t.Helper()
	ctx := context.Background()
	c, err := db.Repos().Get(ctx, "go-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{
		"v1.0.0.zip", "v1.0.0.mod", "v1.1.0.zip", "v1.1.0.mod", "v1.1.0.info", "v1.2.0.zip",
	} {
		body := goModule + " " + file
		if strings.HasSuffix(file, ".info") {
			body = `{"Version":"v1.1.0","Time":"2026-10-16T00:00:00Z"}`
		}
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: goModule + "/@v/" + file},
			Body:   strings.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// goStatusPart is Go's share of the status table, which its pull
// requests (#272–#275) fill in.
var goStatusPart = statusPart{
	handwritten: map[string]bool{
		goList: true, goListHead: true, goInfo: true, goInfoHead: true, goMod: true,
		goModHead: true, goZip: true, goZipHead: true, goLatest: true, goLatestHead: true,
		goLatestInfo: true,
	},
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		seedGoModule(t, &item.Service{Store: db, Blobs: blobs}, db)
	},
	rows: goStatusRows,
}

func goStatusRows() []statusCase {
	m := goBase + "go-local/" + goModule
	v := m + "/@v/"
	sumdb := goBase + "go-local/sumdb/sum.golang.org/supported"
	return []statusCase{
		{goList, v + "list", "admin", "", 200},
		// A caller who may read no version finds none.
		{goList, v + "list", "user", "", 404},
		{goList, v + "list", "anonymous", "", 404},
		{goList, v + "list", "", "", 401},
		{goList, v + "list", "forged", "", 401},
		{goList, goBase + "go-local/example.com/acme/none/@v/list", "admin", "", 404},
		{goList, goBase + "missing/" + goModule + "/@v/list", "admin", "", 404},
		// Another package type's repository is not Go's.
		{goList, goBase + "libs-local/" + goModule + "/@v/list", "admin", "", 404},
		{goList, goBase + "npm-local/" + goModule + "/@v/list", "admin", "", 404},
		// Remote and virtual Go repositories are P9's and P10's.
		{goList, goBase + "go-remote/" + goModule + "/@v/list", "admin", "", 501},
		{goList, goBase + "go-virtual/" + goModule + "/@v/list", "admin", "", 501},
		{goListHead, v + "list", "admin", "", 200},

		// v1.0.0's .info is synthesised, v1.1.0's stored.
		{goInfo, v + "v1.0.0.info", "admin", "", 200},
		{goInfo, v + "v1.1.0.info", "admin", "", 200},
		{goInfo, v + "v1.0.0.info", "user", "", 403},
		{goInfo, v + "v1.0.0.info", "anonymous", "", 401},
		{goInfo, v + "v1.0.0.info", "", "", 401},
		{goInfo, v + "v1.2.0.info", "admin", "", 404},
		{goInfo, v + "main.info", "admin", "", 404},
		{goInfoHead, v + "v1.0.0.info", "admin", "", 200},

		{goMod, v + "v1.0.0.mod", "admin", "", 200},
		{goMod, v + "v1.0.0.mod", "user", "", 403},
		{goMod, v + "v1.0.0.mod", "", "", 401},
		{goModHead, v + "v1.0.0.mod", "admin", "", 200},

		{goZip, v + "v1.0.0.zip", "admin", "", 200},
		{goZip, v + "v1.0.0.zip", "user", "", 403},
		{goZip, v + "v1.0.0.zip", "anonymous", "", 401},
		{goZip, v + "v1.0.0.zip", "expired", "", 401},
		// v1.2.0 has no .mod yet.
		{goZip, v + "v1.2.0.zip", "admin", "", 404},
		{goZip, v + "v1.0.zip", "admin", "", 404},
		{goZipHead, v + "v1.0.0.zip", "admin", "", 200},

		{goLatest, m + "/@latest", "admin", "", 200},
		{goLatest, m + "/@latest", "user", "", 404},
		{goLatest, m + "/@latest", "", "", 401},
		{goLatestHead, m + "/@latest", "admin", "", 200},
		{goLatestInfo, v + "latest.info", "admin", "", 200},
		{goLatestInfo, v + "latest.info", "user", "", 404},

		// A path that names no Go operation is 404 to any method; one that
		// does is 405 to a method no route serves.
		{"GET " + goBase + "{repoKey}/*", m, "admin", "", 404},
		{"POST " + goBase + "{repoKey}/*", m, "admin", "", 404},
		{"POST " + goBase + "{repoKey}/{module}/@v/list", v + "list", "admin", "", 405},
		{
			"DELETE " + goBase + "{repoKey}/{module}/@v/{version}.zip",
			v + "v1.0.0.zip",
			"admin",
			"",
			405,
		},

		// The checksum database is 404 before authentication, to anyone
		// and whatever they present, so the go command consults
		// sum.golang.org itself (decision 0035).
		{goSumDB, sumdb, "", "", 404},
		{goSumDB, sumdb, "anonymous", "", 404},
		{goSumDB, sumdb, "admin", "", 404},
		{goSumDB, sumdb, "forged", "", 404},
		{goSumDB, goBase + "missing/sumdb/sum.golang.org/lookup/x@v1.0.0", "", "", 404},
	}
}
