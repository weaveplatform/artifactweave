package kernel_test

import (
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

type deep struct {
	Arr   [2]time.Time
	Any   any
	None  any
	Ptr   *time.Duration
	Map   map[string][]int
	Empty []int
	Nil   []int
	Bytes []byte
	When  time.Time
	Zero  time.Time
	hide  int
}

func TestUnitCloneAndNormalise(t *testing.T) {
	zone := time.FixedZone("X", 3600)
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, zone)
	d := 1500*time.Microsecond + 7
	in := deep{
		Arr:   [2]time.Time{at, at},
		Any:   []int{1},
		Ptr:   &d,
		Map:   map[string][]int{"k": {1}},
		Empty: []int{},
		Bytes: []byte("state"),
		When:  at,
		hide:  7,
	}
	c := kernel.Clone(in)
	if !c.When.Equal(at) || c.When.Location() != zone || *c.Ptr != d || c.hide != 7 {
		t.Fatalf("Clone changed values: %+v", c)
	}
	c.Map["k"][0] = 9
	c.Any.([]int)[0] = 9 //nolint:forcetypeassert // fixture
	*c.Ptr = 0
	if in.Map["k"][0] != 1 || in.Any.([]int)[0] != 1 ||
		*in.Ptr != d { //nolint:forcetypeassert // fixture
		t.Fatal("Clone shares memory with its input")
	}
	c.Bytes[0] = 'S'
	if string(in.Bytes) != "state" {
		t.Fatalf("Clone shares a byte slice with its input: %q", in.Bytes)
	}
	if c.Empty == nil || c.Nil != nil || c.None != nil {
		t.Fatal("Clone must keep empty and nil apart")
	}

	n := kernel.Normalise(in)
	want := at.UTC().Truncate(time.Microsecond)
	if !n.When.Equal(want) || n.When.Location() != time.UTC || n.Arr[1] != want {
		t.Fatalf("Normalise time = %v, %v", n.When, n.Arr)
	}
	if !n.Zero.IsZero() {
		t.Fatal("a zero time must stay zero")
	}
	if *n.Ptr != time.Millisecond {
		t.Fatalf("Normalise duration = %v", *n.Ptr)
	}
}
