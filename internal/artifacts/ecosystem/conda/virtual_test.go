package conda

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// TestUnitMergerKeepsTheFirstMembersRecordOfEachFile: the merged index of a
// subdir lists every file its members list, each .tar.bz2 and .conda file
// from the first member in the walk that lists it, the copy a download
// through the virtual repository serves, under the merge's own subdir.
func TestUnitMergerKeepsTheFirstMembersRecordOfEachFile(t *testing.T) {
	local, remote := EmptyRepodata("noarch"), EmptyRepodata("noarch")
	local.Packages["a-1-0.tar.bz2"] = Record{Name: "a", License: "local"}
	local.PackagesConda["b-1-0.conda"] = Record{Name: "b", License: "local"}
	remote.Packages["a-1-0.tar.bz2"] = Record{Name: "a", License: "remote"}
	remote.Packages["c-1-0.tar.bz2"] = Record{Name: "c", License: "remote"}
	remote.PackagesConda["b-1-0.conda"] = Record{Name: "b", License: "remote"}
	remote.PackagesConda["d-1-0.conda"] = Record{Name: "d", License: "remote"}
	got, err := Merger{Subdir: "noarch"}.Merge(t.Context(), []virtual.Part[Repodata]{
		{Member: repo.Config{Key: "local"}, Doc: local},
		{Member: repo.Config{Key: "remote"}, Doc: remote},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Info.Subdir != "noarch" || len(got.Packages) != 2 || len(got.PackagesConda) != 2 ||
		got.Packages["a-1-0.tar.bz2"].License != "local" || got.Packages["c-1-0.tar.bz2"].License != "remote" ||
		got.PackagesConda["b-1-0.conda"].License != "local" || got.PackagesConda["d-1-0.conda"].License != "remote" {
		t.Fatalf("merged %+v", got)
	}
	if got, err := (Merger{Subdir: "linux-64"}).Merge(t.Context(), nil); err != nil ||
		got.Info.Subdir != "linux-64" || len(got.Packages)+len(got.PackagesConda) != 0 {
		t.Fatalf("merge of no member = %+v, %v", got, err)
	}
}
