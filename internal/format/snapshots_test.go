package format

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// No limit, or a limit of 0 or less, keeps every build; any other keeps at
// least MinUniqueSnapshots.
func TestUnitUniqueSnapshots(t *testing.T) {
	limit := func(n int) repo.Config { return repo.Config{MaxUniqueSnapshots: &n} }
	for _, c := range []struct {
		c    repo.Config
		want int
	}{
		{repo.Config{}, 0},
		{limit(0), 0},
		{limit(-1), 0},
		{limit(1), MinUniqueSnapshots},
		{limit(2), 2},
		{limit(5), 5},
	} {
		if got := UniqueSnapshots(c.c); got != c.want {
			t.Errorf("%v: %d, want %d", c.c.MaxUniqueSnapshots, got, c.want)
		}
	}
}
