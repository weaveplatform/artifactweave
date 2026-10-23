package server

import (
	"strings"
	"testing"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

const (
	nugetSymbolRead = "GET " + nugetV3 + "{repoKey}/{file}.pdb/{key}/{file}.pdb"
	nugetSymbolHead = "HEAD " + nugetV3 + "{repoKey}/{file}.pdb/{key}/{file}.pdb"
	nugetPDBGUID    = "030201000504070608090A0B0C0D0E0F"
	nugetPDBKey     = nugetPDBGUID + "FFFFFFFF"
	nugetSymbolPath = ".symbols/app.pdb/" + nugetPDBKey + "/app.pdb"
)

func nugetPortablePDB() []byte                     { return support.NuGetPortablePDB() }
func nugetPDBPackage(id string, pdb []byte) []byte { return support.NuGetPDBPackage(id, pdb) }

func seedNuGetSymbols(t *testing.T, db store.Store, blobs blob.Store) {
	t.Helper()
	items := &item.Service{Store: db, Blobs: blobs}
	for _, key := range []string{"nuget-local", "nuget-forced"} {
		deployNuGet(
			t,
			db,
			items,
			key,
			"Debug.1.0.0.snupkg",
			nugetPDBPackage("Debug", nugetPortablePDB()),
		)
	}
	freshNuGetSymbols(t, items)
}

func freshNuGetSymbols(t *testing.T, items *item.Service) {
	t.Helper()
	q, err := work.New(items.Store, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := items.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nuget-local", "nuget-forced"} {
		if err := q.EnsureFresh(t.Context(), nugetfmt.KindSymbols, key); err != nil {
			t.Fatal(err)
		}
	}
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
