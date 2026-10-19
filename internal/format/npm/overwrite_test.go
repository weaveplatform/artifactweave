package npm_test

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tagged is left-pad 1.0.0, then 2.0.0, which takes latest, and the beta
// tag on 2.0.0 too.
func (w *world) tagged() {
	w.t.Helper()
	w.deployVersion("1.0.0")
	w.deployVersion("2.0.0")
	w.annotate(canonical("2.0.0"), types.Properties{npm.PropDistTag: {"latest", "beta"}})
	if got := w.latestOf("left-pad"); got != "2.0.0" {
		w.t.Fatalf("latest %s", got)
	}
}

// converted runs the strict-layout conversion, which moves the tarball
// overwritten onto 2.0.0's path to its own canonical path.
func (w *world) converted() {
	w.t.Helper()
	if err := w.run(npm.KindConvert, ""); err != nil {
		w.t.Fatal(err)
	}
}

// An overwrite with other content leaves the file none of its properties
// (the item service, decision 0034), so the dropped version's dist-tags go
// and latest settles anew (rule 4), and a conversion that later moves the
// file to its own canonical path gives them to no version (#515). Without
// that, 0.5.0 came out of the conversion as latest and beta.
func TestOverwriteDropsDistTags(t *testing.T) {
	w := newWorld(t)
	w.tagged()
	w.deploy(canonical("2.0.0"), pkg("left-pad", "0.5.0", ""), nil)
	if got := w.props(canonical("2.0.0"))[npm.PropDistTag]; got != nil {
		t.Fatalf("the overwritten file kept %v", got)
	}
	if got := w.latestOf("left-pad"); got != "1.0.0" {
		t.Fatalf("latest after the overwrite %s", got)
	}
	w.converted()
	if diff := cmp.Diff(map[string][]string{"1.0.0": {"latest"}, "0.5.0": nil},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("tags after the conversion (-want +got):\n%s", diff)
	}
}

// Content that is no tarball at all drops the version's tags too.
func TestOverwriteWithGarbageDropsDistTags(t *testing.T) {
	w := newWorld(t)
	w.tagged()
	w.deploy(canonical("2.0.0"), []byte("garbage"), nil)
	if got := w.props(canonical("2.0.0"))[npm.PropDistTag]; got != nil {
		t.Fatalf("the overwritten file kept %v", got)
	}
	if got := w.latestOf("left-pad"); got != "1.0.0" {
		t.Fatalf("latest %s", got)
	}
}

// A write that sets dist-tags of its own, as a deploy's matrix parameters
// do (jf rt u --target-props), replaces the file's: those are the new
// content's, so they stay, and the conversion gives them to its version.
func TestOverwriteKeepsTheWritesOwnTags(t *testing.T) {
	w := newWorld(t)
	w.tagged()
	w.deploy(canonical("2.0.0"), pkg("left-pad", "0.5.0", ""),
		types.Properties{npm.PropDistTag: {"legacy"}})
	if got := w.props(canonical("2.0.0"))[npm.PropDistTag]; !cmp.Equal(got, []string{"legacy"}) {
		t.Fatalf("the overwritten file has %v", got)
	}
	w.converted()
	if diff := cmp.Diff(map[string][]string{"1.0.0": {"latest"}, "0.5.0": {"legacy"}},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("tags (-want +got):\n%s", diff)
	}
}

// The dropped version's deprecation goes with its tags, so the version a
// conversion later finds in the file is not shown as deprecated although
// nobody deprecated it (#518).
func TestOverwriteDropsTheDeprecation(t *testing.T) {
	w := newWorld(t)
	w.tagged()
	w.annotate(canonical("2.0.0"), types.Properties{npm.PropDeprecated: {"use 3"}})
	w.deploy(canonical("2.0.0"), pkg("left-pad", "0.5.0", ""), nil)
	if got := w.props(canonical("2.0.0")); len(got) != 0 {
		t.Fatalf("the overwritten file kept %v", got)
	}
	w.converted()
	if m := w.index()["left-pad"]["0.5.0"]; m.Deprecated != "" {
		t.Fatalf("0.5.0 is deprecated: %q", m.Deprecated)
	}
}

// A redeploy of a version with the same content keeps its tags, so it
// undoes no npm dist-tag add. One with other content, a rebuild, is new
// content at the path: its tags go, and latest settles by rule 4, which
// gives it back to 2.0.0, the highest.
func TestRedeployOfAVersion(t *testing.T) {
	w := newWorld(t)
	w.tagged()
	w.annotate(canonical("1.0.0"), types.Properties{npm.PropDistTag: {"latest"}})
	w.deploy(canonical("1.0.0"), pkg("left-pad", "1.0.0", ""), nil)
	if diff := cmp.Diff(map[string][]string{"1.0.0": {"latest"}, "2.0.0": {"beta"}},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("the same content (-want +got):\n%s", diff)
	}
	w.deploy(canonical("1.0.0"), pkg("left-pad", "1.0.0", `"description":"rebuilt"`), nil)
	if diff := cmp.Diff(map[string][]string{"1.0.0": nil, "2.0.0": {"beta", "latest"}},
		w.tags("left-pad")); diff != "" {
		t.Fatalf("a rebuild (-want +got):\n%s", diff)
	}
}

// A copy or a move onto a canonical path replaces the file whole: the
// target has the source's properties and none of its own, so a source
// without dist-tags leaves none behind, and one with its own brings them.
func TestTransferOntoACanonicalPath(t *testing.T) {
	for _, c := range []struct {
		name string
		move bool
		tags []string
	}{
		{"copy", false, nil},
		{"move", true, nil},
		{"copy of a tagged tarball", false, []string{"edge"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			other := repo.Config{Key: "npm-uploads", Class: repo.Local, PackageType: repo.NPM}
			repo.Defaults(&other)
			if err := w.st.Repos().Create(w.ctx, other); err != nil {
				t.Fatal(err)
			}
			var props types.Properties
			if c.tags != nil {
				props = types.Properties{npm.PropDistTag: c.tags}
			}
			// Off its canonical path the source is no version, so it
			// carries only the tags its deploy gave it.
			if _, err := w.items.Deploy(w.ctx, item.Deploy{
				Target:     item.Target{Repo: other, Path: "uploads/left-pad.tgz"},
				Body:       bytes.NewReader(pkg("left-pad", "0.5.0", "")),
				Properties: props,
			}); err != nil {
				t.Fatal(err)
			}
			w.tagged()
			res, err := w.items.Transfer(w.ctx, item.Transfer{
				From: item.Target{Repo: other, Path: "uploads/left-pad.tgz"},
				To:   item.Target{Repo: w.c, Path: canonical("2.0.0")}, Move: c.move,
				CanOverwrite: true,
			})
			if err != nil || res.Failed > 0 {
				t.Fatalf("%+v %v", res, err)
			}
			if got := w.props(canonical("2.0.0"))[npm.PropDistTag]; !cmp.Equal(got, c.tags) {
				t.Fatalf("the target has %v, want %v", got, c.tags)
			}
			if got := w.latestOf("left-pad"); got != "1.0.0" {
				t.Fatalf("latest %s", got)
			}
			w.converted()
			want := map[string][]string{"1.0.0": {"latest"}, "0.5.0": c.tags}
			if diff := cmp.Diff(want, w.tags("left-pad")); diff != "" {
				t.Fatalf("tags after the conversion (-want +got):\n%s", diff)
			}
		})
	}
}
