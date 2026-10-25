package conda

import (
	"errors"
	"net/http/httptest"
	"testing"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
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

func TestUnitCondaRecursionAndPathGuardsBeforeAnySourceRead(t *testing.T) {
	r := httptest.NewRequest("GET", "/artifactory/api/conda/any/noarch/repodata.json", nil)
	h := handler{}
	if _, _, err := h.memberIndex(
		r,
		"../noarch/repodata.json",
		map[string]bool{},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("unsafe index path reached a repository source: %v", err)
	}
	if _, _, err := h.memberIndex(
		r,
		"noarch/repodata.json",
		map[string]bool{"": true},
	); err == nil {
		t.Fatal("recursive index lookup reached a repository source")
	}
	if _, err := h.members(r); err == nil {
		t.Fatal("nonvirtual repository listed virtual members")
	}
	id, _ := condafmt.ParsePackagePath("noarch/demo-1-0.conda")
	if err := h.servePackage(
		httptest.NewRecorder(),
		r,
		id,
		map[string]bool{"": true},
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatalf("recursive package lookup reached a repository source: %v", err)
	}
}

func TestUnitVirtualSourceNeverResolvesAnIndependentItem(t *testing.T) {
	if _, err := (virtualSource{}).Item(
		t.Context(),
		nil,
		repo.Config{},
		"noarch/demo-1-0.conda",
	); !errors.Is(
		err,
		source.ErrUnsupported,
	) {
		t.Fatalf("virtual channel resolved content outside its members: %v", err)
	}
	index := condafmt.EmptyRepodata("noarch")
	for _, path := range []string{"noarch/missing-1-0.conda", "noarch/missing-1-0.tar.bz2"} {
		id, _ := condafmt.ParsePackagePath(path)
		if _, ok := recordOf(index, id); ok {
			t.Fatalf("missing archive %s found in channel", path)
		}
	}
}
