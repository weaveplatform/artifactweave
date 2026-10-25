package gems

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	gemfmt "github.com/weaveplatform/artifactweave/internal/format/gems"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitRubyGemsDetailsAndVersionDocuments(t *testing.T) {
	b, err := os.ReadFile("../../format/gems/testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	p, err := gemfmt.Read(
		context.Background(),
		bytes.NewReader(b),
		int64(len(b)),
		"gems/rake-12.3.1.gem",
	)
	if err != nil {
		t.Fatal(err)
	}
	rows := []entry{{Package: p, at: time.Unix(0, 0)}}
	for _, path := range []string{"api/v1/gems/rake.json", "api/v1/versions/rake.json", "api/v1/versions/rake"} {
		body, media, err := render(path, rows)
		if err != nil || media != "application/json" || !json.Valid(body) ||
			!bytes.Contains(body, []byte(p.SHA256)) {
			t.Fatalf("%s: %q, %s, %v", path, body, media, err)
		}
	}
	for _, path := range []string{"api/v1/gems/../rake.json", "api/v1/versions/../rake", "quick/Marshal.4.8/missing.gemspec.rz"} {
		if _, _, err := render(path, rows); err == nil {
			t.Fatalf("invalid or absent %q resolved", path)
		}
	}
	if body, media, err := render(
		"info/missing",
		rows,
	); err != nil || media != "text/plain; charset=utf-8" ||
		!strings.HasPrefix(string(body), "---\n") {
		t.Fatalf("empty compact info = %q, %q, %v", body, media, err)
	}
}

func TestUnitRubyGemsRejectsUnknownMetadataAndVirtualSourceLookup(t *testing.T) {
	if _, _, err := render("unrecognized-metadata", nil); err == nil {
		t.Fatal("unknown RubyGems metadata rendered")
	}
	_, err := (virtualSource{}).Item(
		t.Context(),
		nil,
		repo.Config{Class: repo.Virtual},
		"gems/rake-1.0.0.gem",
	)
	if !errors.Is(err, source.ErrUnsupported) {
		t.Fatalf("virtual source lookup = %v, want unsupported", err)
	}
}
