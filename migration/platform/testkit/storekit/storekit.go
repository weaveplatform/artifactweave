package storekit

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// Opts compares stored values: empty and nil collections are equivalent,
// and times must be the same instant in the same location, so a backend
// that returns local times instead of UTC fails.
var Opts = cmp.Options{
	cmpopts.EquateEmpty(),
	cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) && a.Location() == b.Location() }),
}

// Base is a fixed instant in a non-UTC zone with sub-microsecond digits, so
// every test that stores a time also proves UTC and precision
// normalisation.
var Base = time.Date(2026, 10, 13, 14, 45, 12, 123456789, time.FixedZone("BST", 3600))

// At returns Base plus n minutes.
func At(n int) time.Time { return Base.Add(time.Duration(n) * time.Minute) }

// Want is what a store returns for v.
func Want[V any](v V) V { return kernel.Normalise(v) }

// Same fails t when got differs from Want(v).
func Same[V any](t *testing.T, what string, v, got V) {
	t.Helper()
	if diff := cmp.Diff(Want(v), got, Opts); diff != "" {
		t.Fatalf("%s (-want +got):\n%s", what, diff)
	}
}

// Must fails the test on error.
func Must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// WantErr asserts errors.Is(err, target).
func WantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

// Get returns v or fails the test.
func Get[V any](t *testing.T) func(V, error) V {
	return func(v V, err error) V {
		t.Helper()
		Must(t, err)
		return v
	}
}

// Pages reads every page of a list with the given page size and returns
// the concatenation, failing when a page is larger than asked for or the
// list does not terminate.
func Pages[V any](
	t *testing.T, size int, list func(kernel.Page) ([]V, string, error),
) []V {
	t.Helper()
	var out []V
	p := kernel.Page{Limit: size}
	for range 10_000 {
		got, next, err := list(p)
		Must(t, err)
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
