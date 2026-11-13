package luarockshttp_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// errStatement is a statement the database failed to run, as the SQL
// store's Wrap passes it up: the driver's error marked kernel.ErrStore.
var errStatement = fmt.Errorf("sqlite: ensure package: %w", errors.Join(kernel.ErrStore, errDropped))

// indexFaults is the item service's store whose package index fails
// with err while it is set.
type indexFaults struct {
	item.Store
	err *error
}

func (s indexFaults) Packages() port.PackageStore {
	return failingEnsure{PackageStore: s.Store.Packages(), err: s.err}
}

type failingEnsure struct {
	port.PackageStore
	err *error
}

func (p failingEnsure) Ensure(ctx context.Context, pkg artmodel.Package) (artmodel.Package, error) {
	if *p.err != nil {
		return artmodel.Package{}, *p.err
	}
	return p.PackageStore.Ensure(ctx, pkg) //nolint:wrapcheck // the store's own error
}

// A remote rock whose publication meets a statement the store failed to
// run, as LuaRocks' Deployed syncs the rock's version into the package
// index, answers 500 "internal error", logs the store's error once and
// caches nothing; the circuit is untouched, and once the store works the
// next request fetches the rock again. It answered 502 "Bad Gateway",
// which a bad upstream rock still answers (#1470).
func TestAccRemoteRockAnswersAStoreFailureUnderItsHookAsAServerFault(t *testing.T) {
	const path = "visible-1.0-1.src.rock"
	var fail error
	var faults []error
	f := newRemoteFixture(t, func(o *pkgroute.Options, items *item.Service) {
		items.Store = indexFaults{Store: items.Store, err: &fail}
		o.Fault = func(_ *http.Request, err error) { faults = append(faults, err) }
	})
	f.seed()
	f.up.Set("/rocks/"+path, fakeupstream.Resource{Body: rock(t, "visible", "1.0-1")})
	wantBody(t, f.do(http.MethodGet, remoteBase+"manifest", nil, "", "admin"), []string{`"1.0-1"`}, nil)
	fail = errStatement
	wantError(t, f.do(http.MethodGet, remoteBase+path, nil, "", "admin"),
		http.StatusInternalServerError, "internal error")
	if len(faults) != 1 || !errors.Is(faults[0], errDropped) {
		t.Fatalf("faults %v", faults)
	}
	if _, err := f.db.Items().Get(t.Context(), "rocks", true, path); err == nil {
		t.Fatal("the rock was cached")
	}
	state, err := f.db.RemoteCache().State(t.Context(), "rocks")
	if err != nil || state.ConnectionFailures != 0 {
		t.Fatalf("circuit %d: %v", state.ConnectionFailures, err)
	}
	fail = nil
	if rec := f.do(http.MethodGet, remoteBase+path, nil, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("cured: %d %s", rec.Code, rec.Body)
	}
	if got := f.up.Count(http.MethodGet, "/rocks/"+path); got != 2 {
		t.Fatalf("upstream fetched the rock %d times, want 2", got)
	}
}
