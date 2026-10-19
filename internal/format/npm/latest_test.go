package npm_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// latestOf is the version of name whose Metadata carries latest, failing
// the test unless exactly one does.
func (w *world) latestOf(name string) string {
	w.t.Helper()
	var holders []string
	for v, tags := range w.tags(name) {
		for _, t := range tags {
			if t == npm.TagLatest {
				holders = append(holders, v)
			}
		}
	}
	if len(holders) != 1 {
		w.t.Fatalf("%s: latest on %v", name, holders)
	}
	return holders[0]
}

func canonical(version string) string { return npm.CanonicalPath("left-pad", version) }

// deployVersion deploys left-pad at version to its canonical path with the
// dist-tags tags, as jf npm publish --tag deploys it.
func (w *world) deployVersion(version string, tags ...string) {
	w.t.Helper()
	var props types.Properties
	if len(tags) > 0 {
		props = types.Properties{npm.PropDistTag: tags}
	}
	w.deploy(canonical(version), pkg("left-pad", version, ""), props)
}

func (w *world) remove(p string) {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: p}, "tester"); err != nil {
		w.t.Fatal(err)
	}
}

// latest is stored as npm.disttag=latest and moves by decision 0034's
// rules, as 7.x moves it: an untagged deploy takes it when it outranks the
// version that has it, a tagged one never does once the package has one,
// npm.disttag=latest set by any route moves it there, and removing it
// hands it to the version that outranks the others.
func TestLatestRules(t *testing.T) {
	w := newWorld(t)
	steps := []struct {
		what string
		do   func()
		want string
	}{
		{
			"a tagged first version takes latest",
			func() { w.deployVersion("1.0.0-beta.1", "beta") },
			"1.0.0-beta.1",
		},
		{"a stable version outranks a pre-release", func() { w.deployVersion("0.9.0") }, "0.9.0"},
		{"an untagged higher version takes it", func() { w.deployVersion("1.0.0") }, "1.0.0"},
		{"an untagged lower version leaves it", func() { w.deployVersion("0.9.5") }, "1.0.0"},
		// RTFACT-24072: npm publish --tag leaves latest where it is.
		{"a tagged higher version leaves it", func() { w.deployVersion("2.0.0", "next") }, "1.0.0"},
		{
			"a pre-release never outranks a release", func() { w.deployVersion("3.0.0-rc.1") },
			"1.0.0",
		},
		// npm dist-tag add 0.9.5 latest, or the properties API.
		{"latest set by property moves it", func() {
			w.annotate(canonical("0.9.5"), types.Properties{npm.PropDistTag: {"latest"}})
		}, "0.9.5"},
		// A pin lasts until the next untagged version that outranks it.
		{"a redeploy leaves a pin", func() { w.deployVersion("1.0.0") }, "0.9.5"},
		{
			"an untagged version above the pin takes it", func() { w.deployVersion("1.1.0") },
			"1.1.0",
		},
		{
			"removing latest's version hands it on", func() { w.remove(canonical("1.1.0")) },
			"2.0.0",
		},
		// npm dist-tag rm latest: the version that outranks the others
		// takes it back.
		{"latest taken off returns", func() {
			w.annotate(canonical("2.0.0"), types.Properties{npm.PropDistTag: {"next"}})
		}, "2.0.0"},
		{"latest taken off a lower pin goes to the highest", func() {
			w.annotate(canonical("0.9.0"), types.Properties{npm.PropDistTag: {"latest"}})
			w.annotate(canonical("0.9.0"), nil, npm.PropDistTag)
		}, "2.0.0"},
		// An overwrite with another version's content drops the version.
		{"an overwrite that drops latest's version hands it on", func() {
			w.deploy(canonical("2.0.0"), pkg("left-pad", "9.9.9", ""), nil)
		}, "1.0.0"},
	}
	for _, s := range steps {
		s.do()
		if got := w.latestOf("left-pad"); got != s.want {
			t.Fatalf("%s: latest %s, want %s", s.what, got, s.want)
		}
	}
	if got := w.props(canonical("1.0.0"))[npm.PropDistTag]; len(got) != 1 || got[0] != "latest" {
		t.Fatalf("1.0.0's dist-tags %v", got)
	}
	// Only pre-releases left: the highest of them.
	for _, v := range []string{"0.9.0", "0.9.5", "1.0.0"} {
		w.remove(canonical(v))
	}
	if got := w.latestOf("left-pad"); got != "3.0.0-rc.1" {
		t.Fatalf("among pre-releases: %s", got)
	}
}

// A reindex keeps latest where the properties have it, and gives a
// package none of whose tarballs carries it the version that outranks the
// others, writing the property as 7.x's indexing does.
func TestReindexSettlesLatest(t *testing.T) {
	w := newWorld(t)
	w.deployVersion("1.0.0")
	w.deployVersion("2.0.0")
	w.annotate(canonical("1.0.0"), types.Properties{npm.PropDistTag: {"latest"}})
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	if got := w.latestOf("left-pad"); got != "1.0.0" {
		t.Fatalf("reindex moved latest to %s", got)
	}
	// Taken off past the hooks, as a restore of an old database might.
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, canonical("1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.Items().PatchProperties(w.ctx, it.ID, nil,
		[]string{npm.PropDistTag}, w.now); err != nil {
		t.Fatal(err)
	}
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	if got := w.latestOf("left-pad"); got != "2.0.0" {
		t.Fatalf("reindex gave latest to %s", got)
	}
	if got := w.props(canonical("2.0.0"))[npm.PropDistTag]; len(got) != 1 {
		t.Fatalf("2.0.0's dist-tags %v", got)
	}
}

// A copy carrying npm.disttag=latest makes its version latest in the
// target (rule 1), a move takes it from the source's package, which
// settles it anew, and a delete of stray tarballs or folders leaves
// latest alone.
func TestLatestAcrossTransfers(t *testing.T) {
	w := newWorld(t)
	other := repo.Config{Key: "npm-other", Class: repo.Local, PackageType: repo.NPM}
	repo.Defaults(&other)
	if err := w.st.Repos().Create(w.ctx, other); err != nil {
		t.Fatal(err)
	}
	w.deployVersion("1.0.0")
	w.deployVersion("2.0.0")
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: other, Path: canonical("3.0.0")},
		Body:   bytes.NewReader(pkg("left-pad", "3.0.0", "")),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := w.items.Transfer(w.ctx, item.Transfer{
		From: item.Target{Repo: w.c, Path: canonical("2.0.0")},
		To:   item.Target{Repo: other, Path: canonical("2.0.0")}, Move: true,
	})
	if err != nil || res.Failed > 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := w.latestOf("left-pad"); got != "1.0.0" {
		t.Fatalf("source's latest %s", got)
	}
	target := w.c
	w.c = other
	if got := w.latestOf("left-pad"); got != "2.0.0" {
		t.Fatalf("target's latest %s", got)
	}
	w.c = target
	w.deploy("left-pad/stray.tgz", pkg("left-pad", "1.0.0", ""), nil)
	w.remove("left-pad/stray.tgz")
	w.remove("left-pad/-")
	if _, err := w.st.Packages().Find(w.ctx, w.c.Key, "left-pad"); err == nil {
		t.Fatal("the package outlived its last version")
	}
}

func TestOutranks(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "0.9.0", true},
		{"0.9.0", "1.0.0", false},
		{"1.0.0", "2.0.0-rc.1", true},
		{"2.0.0-rc.1", "1.0.0", false},
		{"2.0.0-rc.2", "2.0.0-rc.1", true},
		{"1.0.0", "1.0.0", false},
	} {
		if got := npm.Outranks(c.a, c.b); got != c.want {
			t.Errorf("Outranks(%s, %s) = %v", c.a, c.b, got)
		}
	}
	at := func(min int) time.Time { return time.Date(2026, 10, 19, 9, min, 0, 0, time.UTC) }
	cs := []npm.Candidate{
		{Version: "1.0.0", Published: at(1)},
		{Version: "3.0.0-rc.1", Published: at(4)},
		{Version: "2.0.0", Published: at(2)},
		{Version: "1.5.0", Published: at(4)},
	}
	for _, c := range []struct {
		s    npm.Settings
		cs   []npm.Candidate
		want string
	}{
		{npm.Settings{}, cs, "2.0.0"},
		// The version deployed last, pre-releases included; two of one
		// moment by the version order.
		{npm.Settings{TagLatestByPublish: true}, cs, "1.5.0"},
		{npm.Settings{TagLatestByPublish: true}, cs[:2], "3.0.0-rc.1"},
		{npm.Settings{}, nil, ""},
	} {
		if got := c.s.Latest(c.cs); got != c.want {
			t.Errorf("%+v: latest of %v is %s, want %s", c.s, c.cs, got, c.want)
		}
	}
}

// byPublish turns npm.tag_latest_by_publish on for every write through
// the world's item service, as the server's settings do.
func (w *world) byPublish() {
	w.items.Formats = formats.Settings{NPM: npm.Settings{TagLatestByPublish: true}}.For
}

// With npm.tag_latest_by_publish, rules 2 and 4 rank versions by when
// they were last deployed, as 7.x's artifactory.npm.tag.tagLatestByPublish
// makes "the most recently uploaded package" latest: a pre-release
// included (RTFACT-20127), a tagged publish still not (RTFACT-9754, "if
// no tag was specified"), and a removal hands latest to the version
// deployed last.
func TestLatestByPublish(t *testing.T) {
	w := newWorld(t)
	w.byPublish()
	steps := []struct {
		what string
		do   func()
		want string
	}{
		{"the first version takes latest", func() { w.deployVersion("2.0.0") }, "2.0.0"},
		{"a lower version deployed later takes it", func() { w.deployVersion("1.0.0") }, "1.0.0"},
		{
			"so does a pre-release", func() { w.deployVersion("3.0.0-beta.1") },
			"3.0.0-beta.1",
		},
		{
			"a tagged version leaves it", func() { w.deployVersion("4.0.0", "next") },
			"3.0.0-beta.1",
		},
		{"a redeploy leaves it", func() { w.deployVersion("2.0.0") }, "3.0.0-beta.1"},
		// The redeploy made 2.0.0 the version deployed last.
		{
			"removing latest's version hands it to the last deployed",
			func() { w.remove(canonical("3.0.0-beta.1")) }, "2.0.0",
		},
		{"latest taken off returns to the last deployed", func() {
			w.annotate(canonical("1.0.0"), types.Properties{npm.PropDistTag: {"latest"}})
			w.annotate(canonical("1.0.0"), nil, npm.PropDistTag)
		}, "2.0.0"},
	}
	for _, s := range steps {
		s.do()
		if got := w.latestOf("left-pad"); got != s.want {
			t.Fatalf("%s: latest %s, want %s", s.what, got, s.want)
		}
	}
	// A reindex of a package none of whose tarballs carries latest gives
	// it to the version deployed last too.
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, canonical("2.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.Items().PatchProperties(w.ctx, it.ID, nil,
		[]string{npm.PropDistTag}, w.now); err != nil {
		t.Fatal(err)
	}
	if err := w.items.Reindex(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	if got := w.latestOf("left-pad"); got != "2.0.0" {
		t.Fatalf("reindex gave latest to %s", got)
	}
}

// A version moved in keeps its deploy time, so under
// npm.tag_latest_by_publish it takes latest only from a version deployed
// before it, and the strict-layout conversion of old tarballs leaves
// latest on the newest of them whatever order it moves them in.
func TestLatestByPublishAcrossMoves(t *testing.T) {
	w := newWorld(t)
	w.byPublish()
	w.deploy("uploads/a.tgz", pkg("left-pad", "9.0.0", ""), nil)
	w.deploy("uploads/b.tgz", pkg("left-pad", "1.0.0", ""), nil)
	w.deployVersion("0.1.0")
	if err := w.run(npm.KindConvert, ""); err != nil {
		t.Fatal(err)
	}
	if got := w.latestOf("left-pad"); got != "0.1.0" {
		t.Fatalf("latest %s", got)
	}
}
