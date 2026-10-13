package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestPage(t *testing.T) {
	p, err := store.Page{Limit: 0}.Normalize()
	if err != nil || p.Limit != store.DefaultLimit {
		t.Fatalf("zero limit = %+v, %v", p, err)
	}
	if p, _ := (store.Page{Limit: store.MaxLimit + 1}).Normalize(); p.Limit != store.MaxLimit {
		t.Fatalf("over max = %d", p.Limit)
	}
	if _, err := (store.Page{After: "%%"}).Normalize(); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("bad base64 in Normalize: %v", err)
	}
	if parts, err := (store.Page{}).Key(2); parts != nil || err != nil {
		t.Fatalf("first page = %v, %v", parts, err)
	}
	if _, err := (store.Page{After: "%%"}).Key(1); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("bad base64 in Key: %v", err)
	}
	after := store.Cursor("a", "b")
	if _, err := (store.Page{After: after}).Key(3); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("wrong part count: %v", err)
	}
	if parts, err := (store.Page{After: after}).Key(2); err != nil || parts[0] != "a" || parts[1] != "b" {
		t.Fatalf("Key = %v, %v", parts, err)
	}
}

func TestTrim(t *testing.T) {
	key := func(s string) []string { return []string{s} }
	if rows, next := store.Trim([]string{"a", "b"}, 2, key); len(rows) != 2 || next != "" {
		t.Fatalf("last page = %v, %q", rows, next)
	}
	rows, next := store.Trim([]string{"a", "b", "c"}, 2, key)
	if len(rows) != 2 || next != store.Cursor("b") {
		t.Fatalf("full page = %v, %q", rows, next)
	}
}

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

func TestCloneAndNormalise(t *testing.T) {
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
	c := store.Clone(in)
	if !c.When.Equal(at) || c.When.Location() != zone || *c.Ptr != d || c.hide != 7 {
		t.Fatalf("Clone changed values: %+v", c)
	}
	c.Map["k"][0] = 9
	c.Any.([]int)[0] = 9 //nolint:forcetypeassert // fixture
	*c.Ptr = 0
	if in.Map["k"][0] != 1 || in.Any.([]int)[0] != 1 || *in.Ptr != d { //nolint:forcetypeassert // fixture
		t.Fatal("Clone shares memory with its input")
	}
	c.Bytes[0] = 'S'
	if string(in.Bytes) != "state" {
		t.Fatalf("Clone shares a byte slice with its input: %q", in.Bytes)
	}
	if c.Empty == nil || c.Nil != nil || c.None != nil {
		t.Fatal("Clone must keep empty and nil apart")
	}

	n := store.Normalise(in)
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
