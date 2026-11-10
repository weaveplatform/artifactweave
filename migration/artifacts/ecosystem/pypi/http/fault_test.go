package pypihttp_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	pypifmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/pypi"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

var errFault = errors.New("the store failed")

// faulty is a store whose method named fail fails; the rest pass to
// Store.
type faulty struct {
	pkgroute.Store
	fail string
}

func (s faulty) Packages() port.PackageStore {
	return faultyPackages{PackageStore: s.Store.Packages(), fail: s.fail}
}

func (s faulty) Items() port.ItemStore {
	return faultyItems{ItemStore: s.Store.Items(), fail: s.fail}
}

type faultyPackages struct {
	port.PackageStore
	fail string
}

func (p faultyPackages) Find(ctx context.Context, key, name string) (model.Package, error) {
	if p.fail == "Find" {
		return model.Package{}, errFault
	}
	return p.PackageStore.Find(ctx, key, name)
}

func (p faultyPackages) List(
	ctx context.Context,
	key string,
	page kernel.Page,
) ([]model.Package, string, error) {
	if p.fail == "List" {
		return nil, "", errFault
	}
	return p.PackageStore.List(ctx, key, page)
}

func (p faultyPackages) Versions(ctx context.Context, id int64) ([]model.PackageVersion, error) {
	if p.fail == "Versions" {
		return nil, errFault
	}
	return p.PackageStore.Versions(ctx, id)
}

func (p faultyPackages) VersionOf(ctx context.Context, id int64) (model.PackageVersion, error) {
	if p.fail == "VersionOf" {
		return model.PackageVersion{}, errFault
	}
	return p.PackageStore.VersionOf(ctx, id)
}

type faultyItems struct {
	port.ItemStore
	fail string
}

func (s faultyItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (model.Item, error) {
	if s.fail == "Get" {
		return model.Item{}, errFault
	}
	return s.ItemStore.Get(ctx, key, cached, path)
}

// A store that fails answers a bare 500 and the failure goes to the
// access log, whichever read it fails.
func TestAccReadFaults(t *testing.T) {
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
		f := newFixture(
			t,
			pypifmt.Settings{CoreMetadata: true},
			func(s pkgroute.Store) pkgroute.Store {
				return faulty{Store: s, fail: c.fail}
			},
		)
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
func TestAccUnreadableVersion(t *testing.T) {
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
