package pypi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errFault = errors.New("the store failed")

// faulty is a store whose method named fail fails; the rest pass to
// Store.
type faulty struct {
	store.Store
	fail string
}

func (s faulty) Packages() store.PackageStore {
	return faultyPackages{PackageStore: s.Store.Packages(), fail: s.fail}
}

func (s faulty) Items() store.ItemStore {
	return faultyItems{ItemStore: s.Store.Items(), fail: s.fail}
}

type faultyPackages struct {
	store.PackageStore
	fail string
}

func (p faultyPackages) Find(ctx context.Context, key, name string) (types.Package, error) {
	if p.fail == "Find" {
		return types.Package{}, errFault
	}
	return p.PackageStore.Find(ctx, key, name)
}

func (p faultyPackages) List(
	ctx context.Context,
	key string,
	page store.Page,
) ([]types.Package, string, error) {
	if p.fail == "List" {
		return nil, "", errFault
	}
	return p.PackageStore.List(ctx, key, page)
}

func (p faultyPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	if p.fail == "Versions" {
		return nil, errFault
	}
	return p.PackageStore.Versions(ctx, id)
}

func (p faultyPackages) VersionOf(ctx context.Context, id int64) (types.PackageVersion, error) {
	if p.fail == "VersionOf" {
		return types.PackageVersion{}, errFault
	}
	return p.PackageStore.VersionOf(ctx, id)
}

type faultyItems struct {
	store.ItemStore
	fail string
}

func (s faultyItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (types.Item, error) {
	if s.fail == "Get" {
		return types.Item{}, errFault
	}
	return s.ItemStore.Get(ctx, key, cached, path)
}

// A store that fails answers a bare 500 and the failure goes to the
// access log, whichever read it fails.
func TestReadFaults(t *testing.T) {
	for _, c := range []struct{ fail, path string }{
		{"List", api + "simple/"},
		{"Versions", api + "simple/"},
		{"Find", api + "simple/demo/"},
		{"Find", api + "pypi/demo/json"},
		{"Versions", api + "pypi/demo/1.0/json"},
		{"Versions", api + "simple/demo/"},
		{"Get", api + "demo/1.0/demo-1.0-py3-none-any.whl.metadata"},
		{"VersionOf", api + "demo/1.0/demo-1.0-py3-none-any.whl.metadata"},
	} {
		f := newFixture(t, pypifmt.Settings{CoreMetadata: true}, func(s store.Store) store.Store {
			return faulty{Store: s, fail: c.fail}
		})
		seedDemo(t, f)
		rec := f.do(http.MethodGet, c.path, "reader")
		if rec.Code != http.StatusInternalServerError || len(f.faults) != 1 ||
			!errors.Is(f.faults[0], errFault) {
			t.Errorf(
				"%s failing on %s: %d %s, faults %v",
				c.path,
				c.fail,
				rec.Code,
				rec.Body,
				f.faults,
			)
		}
	}
}

// A version whose document the format cannot read fails the reads that
// need it.
func TestUnreadableVersion(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{CoreMetadata: true})
	seedDemo(t, f)
	ctx := context.Background()
	p, err := f.db.Packages().Find(ctx, "pypi-local", "demo")
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.db.Packages().Version(ctx, p.ID, "1.0")
	if err != nil {
		t.Fatal(err)
	}
	v.Metadata = []byte("{not json")
	if _, err := f.db.Packages().PutVersion(ctx, v); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		api + "simple/", api + "simple/demo/", api + "demo/1.0/demo-1.0-py3-none-any.whl.metadata",
		api + "pypi/demo/json",
	} {
		f.faults = nil
		if rec := f.do(
			http.MethodGet,
			path,
			"reader",
		); rec.Code != http.StatusInternalServerError ||
			len(f.faults) != 1 {
			t.Errorf("%s: %d %s, faults %v", path, rec.Code, rec.Body, f.faults)
		}
	}
}
