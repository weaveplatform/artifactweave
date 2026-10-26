// Package storetest is the contract suite for pkg/store implementations
// (decision 0015). Every backend (inmem, SQLite in memory and on disk,
// PostgreSQL) runs Run with a factory that returns a fresh, empty store, so
// one set of expectations defines the behaviour of all of them.
package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Factory returns a fresh, empty store and registers its cleanup with t.
type Factory func(t *testing.T) store.Store

// Run executes the full contract, one subtest per concern, each on its own
// store.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	runCore(t, newStore)
	runReplicas(t, newStore)
	runContent(t, newStore)
	runGitLFSLocks(t, newStore)
	runRemoteCache(t, newStore)
	runIdentity(t, newStore)
	runOperations(t, newStore)
	runWork(t, newStore)
	runPeers(t, newStore)
	runEnterprise(t, newStore)
	runBuilds(t, newStore)
}

// opts compares stored values: empty and nil collections are equivalent,
// and times must be the same instant in the same location, so a backend
// that returns local times instead of UTC fails.
var opts = cmp.Options{
	cmpopts.EquateEmpty(),
	cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) && a.Location() == b.Location() }),
}

// base is a fixed instant in a non-UTC zone with sub-microsecond digits, so
// every test that stores a time also proves UTC and precision
// normalisation.
var base = time.Date(2026, 10, 13, 14, 45, 12, 123456789, time.FixedZone("BST", 3600))

// at returns base plus n minutes.
func at(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

// want is what a store returns for v.
func want[V any](v V) V { return store.Normalise(v) }

// same fails t when got differs from want(v).
func same[V any](t *testing.T, what string, v, got V) {
	t.Helper()
	if diff := cmp.Diff(want(v), got, opts); diff != "" {
		t.Fatalf("%s (-want +got):\n%s", what, diff)
	}
}

// must fails the test on error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// wantErr asserts errors.Is(err, target).
func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

// get returns v or fails the test.
func get[V any](t *testing.T) func(V, error) V {
	return func(v V, err error) V {
		t.Helper()
		must(t, err)
		return v
	}
}

// pages reads every page of a list with the given page size and returns
// the concatenation, failing when a page is larger than asked for or the
// list does not terminate.
func pages[V any](
	t *testing.T, size int, list func(store.Page) ([]V, string, error),
) []V {
	t.Helper()
	var out []V
	p := store.Page{Limit: size}
	for range 10_000 {
		got, next, err := list(p)
		must(t, err)
		if len(got) > size {
			t.Fatalf("page of %d items, asked for %d", len(got), size)
		}
		out = append(out, got...)
		if next == "" {
			return out
		}
		p.After = next
	}
	t.Fatal("list did not terminate")
	return nil
}
