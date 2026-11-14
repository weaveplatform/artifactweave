package kernel_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitPage(t *testing.T) {
	p, err := kernel.Page{Limit: 0}.Normalize()
	if err != nil || p.Limit != kernel.DefaultLimit {
		t.Fatalf("zero limit = %+v, %v", p, err)
	}
	if p, _ := (kernel.Page{Limit: kernel.MaxLimit + 1}).Normalize(); p.Limit != kernel.MaxLimit {
		t.Fatalf("over max = %d", p.Limit)
	}
	if _, err := (kernel.Page{After: "%%"}).Normalize(); !errors.Is(err, kernel.ErrInvalidCursor) {
		t.Fatalf("bad base64 in Normalize: %v", err)
	}
	if parts, err := (kernel.Page{}).Key(2); parts != nil || err != nil {
		t.Fatalf("first page = %v, %v", parts, err)
	}
	if _, err := (kernel.Page{After: "%%"}).Key(1); !errors.Is(err, kernel.ErrInvalidCursor) {
		t.Fatalf("bad base64 in Key: %v", err)
	}
	after := kernel.Cursor("a", "b")
	if _, err := (kernel.Page{After: after}).Key(3); !errors.Is(err, kernel.ErrInvalidCursor) {
		t.Fatalf("wrong part count: %v", err)
	}
	if parts, err := (kernel.Page{After: after}).Key(
		2,
	); err != nil || parts[0] != "a" ||
		parts[1] != "b" {
		t.Fatalf("Key = %v, %v", parts, err)
	}
}

func TestUnitTrim(t *testing.T) {
	key := func(s string) []string { return []string{s} }
	if rows, next := kernel.Trim([]string{"a", "b"}, 2, key); len(rows) != 2 || next != "" {
		t.Fatalf("last page = %v, %q", rows, next)
	}
	rows, next := kernel.Trim([]string{"a", "b", "c"}, 2, key)
	if len(rows) != 2 || next != kernel.Cursor("b") {
		t.Fatalf("full page = %v, %q", rows, next)
	}
}

// TestUnitPages: a walk reads each page once, in order, ends once the
// PageFunc returns no cursor and keeps its place after a failed read.
func TestUnitPages(t *testing.T) {
	list := []string{"a", "b", "c", "d", "e"}
	fail := false
	var reads []string
	read := func(_ context.Context, p kernel.Page) ([]string, string, error) {
		if fail {
			return nil, "", errors.New("boom")
		}
		reads = append(reads, p.After)
		from := 0
		if parts, _ := p.Key(1); parts != nil {
			from = slices.Index(list, parts[0]) + 1
		}
		page, next := kernel.Trim(
			list[from:],
			p.Limit,
			func(s string) []string { return []string{s} },
		)
		return page, next, nil
	}
	pages := kernel.NewPages(read, 2)
	var got []string
	for i := 0; ; i++ {
		if i == 1 {
			fail = true
			if _, _, err := pages.Next(context.Background()); err == nil {
				t.Fatal("failed read reported no error")
			}
			fail = false
		}
		rows, ok, err := pages.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, rows...)
	}
	if !slices.Equal(got, list) || len(reads) != 3 {
		t.Fatalf("walk = %v in %d reads", got, len(reads))
	}
	if rows, ok, err := pages.Next(context.Background()); rows != nil || ok || err != nil {
		t.Fatalf("after the end = %v, %v, %v", rows, ok, err)
	}
}
