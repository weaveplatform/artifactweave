package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/api"
	"github.com/weaveplatform/artifactweave/internal/specoracle"
)

// specFiles must match tools/specoracle so the test and the report agree.
// specFiles is every bundled service spec; a new service is compared
// without editing this test.
var specFiles = api.SpecFiles

// TestAccSpecParity fails when the authored specs lose parity with JFrog's
// documented operations beyond what api/parity-baseline.json accepts. The
// oracle cache is gitignored, so without it the test skips; CI fetches it in
// the parity job (docs/testing/ci.md).
func TestAccSpecParity(t *testing.T) {
	root := moduleRoot(t)
	cache := filepath.Join(root, ".specoracle")
	if _, err := os.Stat(filepath.Join(cache, specoracle.ManifestFile)); err != nil {
		t.Skip("no oracle cache: run make oracle-fetch")
	}
	theirs, err := specoracle.Load(cache)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(specFiles))
	for _, f := range specFiles {
		paths = append(paths, filepath.Join(root, "api", f))
	}
	ours, err := specoracle.LoadOurs(paths...)
	if err != nil {
		t.Fatal(err)
	}
	report := specoracle.Compare(theirs, ours)
	for _, e := range report.Errors {
		t.Logf("parse problem: %s", e)
	}
	baseline, err := specoracle.ReadBaseline(filepath.Join(root, "api", "parity-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := report.Diff(baseline)
	for _, imp := range d.Improvements {
		t.Logf("improvement (shrink the baseline with make parity-baseline): %s", imp)
	}
	for _, reg := range d.Regressions {
		t.Errorf("parity regression: %s", reg)
	}
	t.Logf("\n%s", report.String())
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
