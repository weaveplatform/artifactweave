package cargo

import (
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestUnitSparseUpstreamMetadataValidation(t *testing.T) {
	for _, raw := range []string{"", "{", `{"dl":"https://host.example/#fragment"}`, `{"dl":"https://user@host.example/crates"}`, `{"dl":"https://host.example/%zz"}`} {
		if _, err := parseConfig([]byte(raw)); !errors.Is(err, source.ErrInvalidResult) {
			t.Errorf("config %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "not json\n", `{"name":"other"}` + "\n", strings.Repeat("x", 1<<20+1) + "\n"} {
		if _, err := parseIndex(
			[]byte(raw),
			"hello_cargo",
		); !errors.Is(
			err,
			source.ErrInvalidResult,
		) {
			t.Errorf("index %d bytes: %v", len(raw), err)
		}
	}
	if _, err := parseIndex(
		[]byte(strings.Repeat("{}\n", 10001)),
		"hello_cargo",
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("unbounded sparse version list: %v", err)
	}
	if _, err := downloadURL(
		"https://host.example/crates",
		"bad/name",
		"1.0.0",
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatalf("invalid name: %v", err)
	}
	if _, err := downloadURL(
		"https://host.example/crates",
		"hello",
		"invalid",
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatalf("invalid version: %v", err)
	}
	if _, err := downloadURL(
		"https://host.example/%zz",
		"hello",
		"1.0.0",
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("invalid URL: %v", err)
	}
	got, err := downloadURL(
		"https://host.example/{lowerprefix}/{crate}/{version}",
		"HelloWorld",
		"1.0.0",
	)
	if err != nil || got != "https://host.example/he/ll/HelloWorld/1.0.0" {
		t.Fatalf("templated URL: %q %v", got, err)
	}
}
