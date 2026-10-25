package npm_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// enforcing turns npm.enforce_path on for every write through the world's
// item service, as the server's settings do.
func (w *world) enforcing() {
	w.items.Formats = formats.Settings{NPM: npm.Settings{EnforcePath: true}}.For
}

// With npm.enforce_path, a package tarball uploaded under a file name its
// package.json does not give is refused with 400, as 7.x's
// artifactory.npm.enforce.path rejects "uploading packages with file names
// that do not match the internal metadata of the package". The folders
// before the file name are not compared, and content without a readable
// package.json, or a file that is no .tgz, names no package to compare.
func TestUnitEnforcePath(t *testing.T) {
	w := newWorld(t)
	w.enforcing()
	for _, c := range []struct {
		path    string
		body    []byte
		refused bool
	}{
		{"left-pad/-/left-pad-1.0.0.tgz", pkg("left-pad", "1.0.0", ""), false},
		{"elsewhere/left-pad-1.0.0.tgz", pkg("left-pad", "1.0.0", ""), false},
		{"left-pad-1.0.0.tgz", pkg("left-pad", "1.0.0", ""), false},
		{"left-pad/-/left-pad-2.0.0.tgz", pkg("left-pad", "v2.0.0+build.1", ""), false},
		{"@scope/pkg/-/@scope/pkg-1.0.0.tgz", pkg("@scope/pkg", "1.0.0", ""), false},
		{"uploads/left-pad.tgz", pkg("left-pad", "1.0.0", ""), true},
		{"left-pad/-/left-pad-1.0.1.tgz", pkg("left-pad", "1.0.0", ""), true},
		{"left-pad/-/right-pad-1.0.0.tgz", pkg("left-pad", "1.0.0", ""), true},
		{"@scope/pkg/-/pkg-1.0.0.tgz", pkg("@scope/pkg", "1.0.0", ""), true},
		{"@other/pkg-1.0.0.tgz", pkg("@scope/pkg", "1.0.0", ""), true},
		{"junk.tgz", []byte("no tarball"), false},
		{"uploads/left-pad.tar.gz", pkg("left-pad", "1.0.0", ""), false},
	} {
		_, err := w.tryDeploy(c.path, c.body, nil)
		var refused *format.Rejection
		switch {
		case !c.refused && err != nil:
			t.Errorf("%s: %v", c.path, err)
		case c.refused && (!errors.As(err, &refused) || refused.Status != http.StatusBadRequest):
			t.Errorf("%s: %v, want a 400", c.path, err)
		}
	}
	_, err := w.tryDeploy("uploads/left-pad.tgz", pkg("left-pad", "1.0.0", ""), nil)
	if want := "The repository 'npm-local' rejected 'uploads/left-pad.tgz': the file name of " +
		"npm package left-pad@1.0.0 must be left-pad-1.0.0.tgz."; err == nil || err.Error() != want {
		t.Errorf("message %v, want %q", err, want)
	}
	if _, ok := w.index()["left-pad"]["1.0.0"]; !ok {
		t.Error("the canonical tarball is not indexed")
	}
}

// Without npm.enforce_path, the default, a tarball under any name is
// stored, as strict layout stores it.
func TestUnitEnforcePathOff(t *testing.T) {
	w := newWorld(t)
	if _, err := w.tryDeploy(
		"uploads/left-pad.tgz",
		pkg("left-pad", "1.0.0", ""),
		nil,
	); err != nil {
		t.Fatal(err)
	}
}

// npm.enforce_path checks uploads only: a copy or a move lands the file
// it carries under any name, and a file stored before the setting was on
// stays where it is. 7.x documents uploads alone (#216).
func TestUnitEnforcePathLeavesCopiesAndMoves(t *testing.T) {
	w := newWorld(t)
	w.deploy("uploads/left-pad.tgz", pkg("left-pad", "1.0.0", ""), nil)
	w.enforcing()
	for _, move := range []bool{false, true} {
		dst := "copied/left-pad.tgz"
		if move {
			dst = "moved/left-pad.tgz"
		}
		res, err := w.items.Transfer(w.ctx, item.Transfer{
			From: item.Target{Repo: w.c, Path: "uploads/left-pad.tgz"},
			To:   item.Target{Repo: w.c, Path: dst}, Move: move,
		})
		if err != nil || res.Failed > 0 {
			t.Fatalf("move %v: %+v %v", move, res, err)
		}
	}
}

// Only a local or federated repository's uploads are checked, and never a
// remote repository's cache.
func TestUnitEnforcePathRepositories(t *testing.T) {
	w := newWorld(t)
	tarball := w.deploy("x/-/x-1.0.0.tgz", pkg("x", "1.0.0", ""), nil)
	h := npm.Handler{Settings: npm.Settings{EnforcePath: true}}
	for _, c := range []struct {
		class   repo.Class
		cached  bool
		refused bool
	}{
		{repo.Local, false, true},
		{repo.Federated, false, true},
		{repo.Remote, true, false},
		{repo.Virtual, false, false},
	} {
		cfg := repo.Config{Key: "npm-x", Class: c.class, PackageType: repo.NPM}
		wr := format.Write{
			Op: format.OpDeploy, Path: "y.tgz", Cached: c.cached, Checksums: tarball.Actual,
		}
		in, err := h.Inspect(context.Background(), format.Env{Blobs: w.items.Blobs}, cfg, wr)
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.Admit(context.Background(), format.Env{Inspection: in}, cfg, wr)
		if (err != nil) != c.refused {
			t.Errorf("%s cached %v: %v", c.class, c.cached, err)
		}
	}
}

// A publish always lands its tarball at the canonical path its own
// package.json gives, so npm.enforce_path never refuses one.
func TestUnitEnforcePathAdmitsAPublish(t *testing.T) {
	w := newWorld(t)
	w.enforcing()
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: w.c, Path: npm.CanonicalPath("@s/x", "1.0.0")},
		Body:   bytes.NewReader(pkg("@s/x", "1.0.0", "")), IfAbsent: true,
		Properties: types.Properties{npm.PropDistTag: {"beta"}},
	}); err != nil {
		t.Fatal(err)
	}
}
