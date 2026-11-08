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

// Want returns what a store returns for v: kernel.Normalise's deep copy,
// with times in UTC truncated to microseconds and durations truncated to
// milliseconds.
func Want[V any](v V) V { return kernel.Normalise(v) }

// Same stops the test when got, the value read back, differs under Opts
// from Want(v), v being the value written; the message names what and
// gives the -want +got diff.
func Same[V any](t *testing.T, what string, v, got V) {
	t.Helper()
	if diff := cmp.Diff(Want(v), got, Opts); diff != "" {
		t.Fatalf("%s (-want +got):\n%s", what, diff)
	}
}

// Must stops the test with err when err is not nil.
func Must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// WantErr stops the test unless errors.Is(err, target), printing both.
func WantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

// Get returns a function that stops the test when a call's error is not nil
// and otherwise returns the call's value, so Get[V](t)(call()) reads a value
// from a call that must succeed.
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
