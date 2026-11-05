package kernel_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
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
