package conda

import (
	"testing"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
)

func TestUnitRecordOfUsesArchiveKind(t *testing.T) {
	index := condafmt.EmptyRepodata("noarch")
	index.Packages["sample-1-0.tar.bz2"] = condafmt.Record{Name: "legacy"}
	index.PackagesConda["sample-1-0.conda"] = condafmt.Record{Name: "modern"}
	for _, tc := range []struct {
		path, want string
	}{
		{"noarch/sample-1-0.tar.bz2", "legacy"},
		{"noarch/sample-1-0.conda", "modern"},
	} {
		id, ok := condafmt.ParsePackagePath(tc.path)
		if !ok {
			t.Fatalf("package path %q rejected", tc.path)
		}
		rec, found := recordOf(index, id)
		if !found || rec.Name != tc.want {
			t.Errorf("%s: %+v, found %t", tc.path, rec, found)
		}
	}
}
