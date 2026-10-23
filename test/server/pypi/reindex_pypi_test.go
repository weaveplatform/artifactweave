package pypi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
)

// PyPI's reindex runs the PyPI format (#537): it rebuilds a repository's
// index from its distributions, from an empty index too, before it
// answers.
func TestPypiReindexRebuilds(t *testing.T) {
	ctx := context.Background()
	w := newPypiReindexWorld(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("PKG-INFO")
	_, _ = io.WriteString(f, "Metadata-Version: 2.1\nName: Demo_Pkg\nVersion: 1.0\n")
	_ = zw.Close()
	wantStatus(t, w.do("PUT", "/artifactory/pypi-local/uploads/demo_pkg-1.0.zip", "admin",
		buf.String()), http.StatusCreated)
	// indexed reports whether the index holds demo-pkg 1.0 and nothing
	// else.
	indexed := func() bool {
		t.Helper()
		pkgs, _, err := w.db.Packages().List(ctx, "pypi-local", store.Page{Limit: store.MaxLimit})
		if err != nil {
			t.Fatal(err)
		}
		if len(pkgs) != 1 || pkgs[0].Name != "demo-pkg" {
			return false
		}
		vs, err := w.db.Packages().Versions(ctx, pkgs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(vs) == 1 && vs[0].Version == "1.0"
	}
	if !indexed() {
		t.Fatal("the deploy left no demo-pkg 1.0")
	}
	p, err := w.db.Packages().Find(ctx, "pypi-local", "demo-pkg")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Packages().Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	wantText(t, w.do("POST", "/artifactory/api/pypi/pypi-local/reindex", "deployer", ""),
		"Recalculating index for pypi repository pypi-local scheduled to run")
	if !indexed() {
		t.Fatal("the reindex did not rebuild demo-pkg 1.0")
	}
}
