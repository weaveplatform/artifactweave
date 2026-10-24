package helm_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// world is a Helm local repository with the item service over it and a
// clock that moves a minute on every write.
type world struct {
	t     *testing.T
	ctx   context.Context
	st    store.Store
	items *item.Service
	c     repo.Config
	now   time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return newWorldOn(t, inmem.New())
}

func newWorldOn(t *testing.T, st store.Store) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, ctx: context.Background(), st: st,
		now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
	}
	w.items = &item.Service{Store: st, Blobs: blobs, Now: func() time.Time { return w.now }}
	w.c = w.repo("helm-local", nil)
	return w
}

// repo creates a local Helm repository called key, as tune leaves it.
func (w *world) repo(key string, tune func(*repo.Config)) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.Helm}
	repo.Defaults(&c)
	if tune != nil {
		tune(&c)
	}
	if err := w.st.Repos().Create(w.ctx, c); err != nil {
		w.t.Fatal(err)
	}
	return c
}

func (w *world) deploy(p string, body []byte) types.Item {
	w.t.Helper()
	it, err := w.deployTo(w.c, p, body)
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

func (w *world) deployTo(c repo.Config, p string, body []byte) (types.Item, error) {
	w.now = w.now.Add(time.Minute)
	return w.items.Deploy(w.ctx, item.Deploy{ //nolint:wrapcheck // test helper
		Target: item.Target{Repo: c, Path: p}, Body: bytes.NewReader(body), By: "tester",
	})
}

func (w *world) remove(p string) {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: p}, "tester"); err != nil {
		w.t.Fatal(err)
	}
}

// transfer copies or moves from:src to to:dst and returns the report.
func (w *world) transfer(
	from repo.Config,
	src string,
	to repo.Config,
	dst string,
	move bool,
) item.TransferResult {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	res, err := w.items.Transfer(w.ctx, item.Transfer{
		From: item.Target{Repo: from, Path: src}, To: item.Target{Repo: to, Path: dst},
		Move: move, CanOverwrite: true, By: "tester",
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return res
}

func (w *world) props(p string) types.Properties {
	w.t.Helper()
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	props, err := w.st.Items().Properties(w.ctx, it.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return props
}

// entry is a version as the index holds it.
type entry struct {
	Meta  helm.Metadata
	Files []string
}

// index is c's index: each package's versions.
func (w *world) index(c repo.Config) map[string]map[string]entry {
	w.t.Helper()
	out := map[string]map[string]entry{}
	pkgs, _, err := w.st.Packages().List(w.ctx, c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, p := range pkgs {
		vs, err := w.st.Packages().Versions(w.ctx, p.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		out[p.Name] = map[string]entry{}
		for _, v := range vs {
			m, err := helm.DecodeMetadata(v.Metadata)
			if err != nil {
				w.t.Fatal(err)
			}
			if !v.Created.Equal(m.Created) {
				w.t.Errorf("%s %s is dated %s and described as created %s",
					p.Name, v.Version, v.Created, m.Created)
			}
			ids, err := w.st.Packages().Files(w.ctx, v.ID)
			if err != nil {
				w.t.Fatal(err)
			}
			e := entry{Meta: m}
			for _, id := range ids {
				it, err := w.st.Items().GetByID(w.ctx, id)
				if err != nil {
					w.t.Fatal(err)
				}
				e.Files = append(e.Files, it.FullPath())
			}
			slices.Sort(e.Files)
			out[p.Name][v.Version] = e
		}
	}
	return out
}

// described checks that version v of chart name in c is described by the
// archive at p, holding description, and links files.
func (w *world) described(c repo.Config, name, v, p, description string, files ...string) {
	w.t.Helper()
	e, ok := w.index(c)[name][v]
	if !ok {
		w.t.Fatalf("%s %s is not indexed: %v", name, v, w.index(c))
	}
	it, err := w.st.Items().Get(w.ctx, c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	slices.Sort(files)
	if e.Meta.Path != p || e.Meta.Digest != it.Actual.SHA256 ||
		!e.Meta.Created.Equal(it.Modified) || e.Meta.Chart.Description != description ||
		!slices.Equal(e.Files, files) {
		w.t.Fatalf("%s %s: %+v, want %s (%s) linking %v", name, v, e, p, description, files)
	}
}

// queued lists the keys of the queued items of kind.
func (w *world) queued(kind string) []string {
	w.t.Helper()
	var out []string
	items, err := w.st.WorkQueue().Lease(w.ctx, store.WorkLease{
		Holder: "test",
		Limit:  store.MaxLimit,
		Until:  w.now.Add(2 * time.Hour),
		Now:    w.now.Add(time.Hour),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it.Key)
		}
		if err := w.st.WorkQueue().
			Complete(w.ctx, it.Kind, it.Key, it.Generation, w.now); err != nil {
			w.t.Fatal(err)
		}
	}
	slices.Sort(out)
	return out
}

// A deployed chart is a version of the package its name gives, described
// by its Chart.yaml with its path, digest and deploy time, and carries the
// chart.* properties; its .prov is an ordinary file of no version.
func TestUnitDeployIndexesACharts(t *testing.T) {
	w := newWorld(t)
	it := w.deploy(
		"team/web-1.0.0.tgz",
		chartTgz("web", "1.0.0", "appVersion: 2.4", "description: web"),
	)
	w.deploy("team/web-1.0.0.tgz.prov", []byte("signed"))
	w.deploy("team/other.txt", []byte("text"))
	w.described(w.c, "web", "1.0.0", "team/web-1.0.0.tgz", "web", "team/web-1.0.0.tgz")
	e := w.index(w.c)["web"]["1.0.0"]
	want := helm.Chart{
		APIVersion: "v2", Name: "web", Version: "1.0.0", AppVersion: "2.4", Description: "web",
	}
	if diff := cmp.Diff(want, e.Meta.Chart); diff != "" || e.Meta.Digest != it.Actual.SHA256 {
		t.Fatal(diff, e.Meta.Digest)
	}
	if diff := cmp.Diff(types.Properties{
		helm.PropName: {"web"}, helm.PropVersion: {"1.0.0"}, helm.PropAppVersion: {"2.4"},
	}, w.props("team/web-1.0.0.tgz")); diff != "" {
		t.Fatal(diff)
	}
	if got := w.props("team/web-1.0.0.tgz.prov"); len(got) != 0 {
		t.Fatal(got)
	}
	if len(w.index(w.c)) != 1 {
		t.Fatal(w.index(w.c))
	}
}

// Without Enforce Layout the same name and version at several paths is one
// version linking each archive, described by the last deployed, as 7.x
// "retains only the latest entry and overwrites the URL". When that one
// goes, the next newest describes it, and the version goes with the last.
func TestUnitDuplicatesNewestWins(t *testing.T) {
	w := newWorld(t)
	w.deploy("a/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: a"))
	w.deploy("b/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: b"))
	w.deploy("c/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: c"))
	all := []string{"a/web-1.0.0.tgz", "b/web-1.0.0.tgz", "c/web-1.0.0.tgz"}
	w.described(w.c, "web", "1.0.0", "c/web-1.0.0.tgz", "c", all...)
	// An older archive leaving changes nothing but the links.
	w.remove("b/web-1.0.0.tgz")
	w.described(w.c, "web", "1.0.0", "c/web-1.0.0.tgz", "c", "a/web-1.0.0.tgz", "c/web-1.0.0.tgz")
	// The newest leaving hands the version to the next.
	w.remove("c/web-1.0.0.tgz")
	w.described(w.c, "web", "1.0.0", "a/web-1.0.0.tgz", "a", "a/web-1.0.0.tgz")
	// An older archive deployed again is the newest now.
	w.deploy("c/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: c2"))
	w.described(w.c, "web", "1.0.0", "c/web-1.0.0.tgz", "c2", "a/web-1.0.0.tgz", "c/web-1.0.0.tgz")
	w.remove("a")
	w.remove("c")
	if got := w.index(w.c); len(got) != 0 {
		t.Fatal(got)
	}
}

// A folder delete takes every archive of a version at once.
func TestUnitDeleteAFolderOfDuplicates(t *testing.T) {
	w := newWorld(t)
	w.deploy("a/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	w.deploy("a/b/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: b"))
	w.deploy("keep/web-2.0.0.tgz", chartTgz("web", "2.0.0"))
	w.remove("a")
	if got := w.index(
		w.c,
	); len(got["web"]) != 1 ||
		got["web"]["2.0.0"].Meta.Path != "keep/web-2.0.0.tgz" {
		t.Fatal(got)
	}
}

// An overwrite changes what a file is a version of: content that is no
// chart leaves its version, which goes when it was the last archive; another
// chart leaves the old version to its other archives.
func TestUnitOverwrite(t *testing.T) {
	w := newWorld(t)
	w.deploy("x/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: x"))
	w.deploy("y/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: y"))
	// The newest becomes another chart: the older one describes 1.0.0.
	w.deploy("y/web-1.0.0.tgz", chartTgz("api", "3.0.0"))
	w.described(w.c, "web", "1.0.0", "x/web-1.0.0.tgz", "x", "x/web-1.0.0.tgz")
	w.described(w.c, "api", "3.0.0", "y/web-1.0.0.tgz", "", "y/web-1.0.0.tgz")
	if got := w.props(
		"y/web-1.0.0.tgz",
	); got[helm.PropName][0] != "api" ||
		got.Has(helm.PropAppVersion, "") {
		t.Fatal(got)
	}
	// The last archive of web becomes no chart: web is gone.
	w.queued(helm.KindIndex)
	w.deploy("x/web-1.0.0.tgz", []byte("not a chart any more"))
	if got := w.index(w.c); len(got) != 1 || got["api"] == nil {
		t.Fatal(got)
	}
	if got := w.queued(helm.KindIndex); !slices.Equal(got, []string{w.c.Key}) {
		t.Fatalf("a chart replaced by other content queues the index: %v", got)
	}
	// A redeploy of the same chart keeps it.
	w.deploy("y/web-1.0.0.tgz", chartTgz("api", "3.0.0", "description: again"))
	w.described(w.c, "api", "3.0.0", "y/web-1.0.0.tgz", "again", "y/web-1.0.0.tgz")
	// The same chart, while another archive of the same version remains.
	w.deploy("z/api-3.0.0.tgz", chartTgz("api", "3.0.0", "description: z"))
	w.deploy("y/web-1.0.0.tgz", chartTgz("api", "4.0.0"))
	w.described(w.c, "api", "3.0.0", "z/api-3.0.0.tgz", "z", "z/api-3.0.0.tgz")
	w.described(w.c, "api", "4.0.0", "y/web-1.0.0.tgz", "", "y/web-1.0.0.tgz")
}

// A copy keeps its source's deploy time, so it ties with its source; the
// copy, made later, describes the version. A move takes the chart with its
// .prov, and the source repository's version with it.
func TestUnitCopyAndMove(t *testing.T) {
	w := newWorld(t)
	other := w.repo("helm-other", nil)
	w.deploy("a/web-1.0.0.tgz", chartTgz("web", "1.0.0", "description: a"))
	w.deploy("a/web-1.0.0.tgz.prov", []byte("signed"))
	if res := w.transfer(w.c, "a/web-1.0.0.tgz", w.c, "b/web-1.0.0.tgz", false); res.Failed > 0 {
		t.Fatal(res.Messages)
	}
	w.described(w.c, "web", "1.0.0", "b/web-1.0.0.tgz", "a", "a/web-1.0.0.tgz", "b/web-1.0.0.tgz")
	if _, err := w.st.Items().Get(w.ctx, w.c.Key, false, "b/web-1.0.0.tgz.prov"); err != nil {
		t.Fatal("the .prov did not go with its chart:", err)
	}
	if res := w.transfer(w.c, "b/web-1.0.0.tgz", other, "m/web-1.0.0.tgz", true); res.Failed > 0 {
		t.Fatal(res.Messages)
	}
	w.described(w.c, "web", "1.0.0", "a/web-1.0.0.tgz", "a", "a/web-1.0.0.tgz")
	w.described(other, "web", "1.0.0", "m/web-1.0.0.tgz", "a", "m/web-1.0.0.tgz")
	for c, p := range map[string]string{w.c.Key: "b/web-1.0.0.tgz.prov", other.Key: "m/web-1.0.0.tgz.prov"} {
		_, err := w.st.Items().Get(w.ctx, c, false, p)
		if (c == other.Key) != (err == nil) {
			t.Fatalf("%s:%s: %v", c, p, err)
		}
	}
}

// A restore of a chart from the trash can, which reads its content again,
// indexes it, with its .prov.
func TestUnitRestore(t *testing.T) {
	w := newWorld(t)
	if err := w.st.Settings().Put(w.ctx, types.SettingTrashcan, types.TrashcanSettings{
		Enabled: true, RetentionDays: 14,
	}); err != nil {
		t.Fatal(err)
	}
	w.deploy("a/web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	w.deploy("a/web-1.0.0.tgz.prov", []byte("signed"))
	w.remove("a/web-1.0.0.tgz")
	if got := w.index(w.c); len(got) != 0 {
		t.Fatal(got)
	}
	tr := &trash.Service{Store: w.st, Items: w.items, Now: func() time.Time { return w.now }}
	if _, err := tr.Restore(
		w.ctx,
		w.c.Key,
		"a/web-1.0.0.tgz",
		trash.Destination{Repo: w.c},
		"tester",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	w.described(w.c, "web", "1.0.0", "a/web-1.0.0.tgz", "", "a/web-1.0.0.tgz")
	if _, err := w.st.Items().Get(w.ctx, w.c.Key, false, "a/web-1.0.0.tgz.prov"); err != nil {
		t.Fatal(err)
	}
}

// The writes that change a repository's charts queue its index.yaml, one
// item keyed by the repository; other writes queue nothing (decision 0039).
// A client's root index.yaml queues it too, and one in another folder is
// an ordinary file.
func TestUnitQueueing(t *testing.T) {
	w := newWorld(t)
	fed := w.repo("helm-fed", func(c *repo.Config) { c.Class = repo.Federated })
	for _, c := range []struct {
		what  string
		write func()
		want  []string
	}{
		{"a chart", func() { w.deploy("web-1.0.0.tgz", chartTgz("web", "1.0.0")) }, []string{w.c.Key}},
		{"a .prov", func() { w.deploy("web-1.0.0.tgz.prov", []byte("signed")) }, nil},
		{"a .tgz with no chart", func() { w.deploy("junk.tgz", []byte("junk")) }, nil},
		{"another file", func() { w.deploy("README.md", []byte("hi")) }, nil},
		{"the root index.yaml", func() { w.deploy("index.yaml", []byte("apiVersion: v1\n")) }, []string{w.c.Key}},
		{"a folder's index.yaml", func() { w.deploy("sub/index.yaml", []byte("x")) }, nil},
		{"the root index.yaml deleted", func() { w.remove("index.yaml") }, []string{w.c.Key}},
		{"a folder's index.yaml deleted", func() { w.remove("sub/index.yaml") }, nil},
		{"a non-chart deleted", func() { w.remove("junk.tgz") }, nil},
		{"a chart deleted", func() { w.remove("web-1.0.0.tgz") }, []string{w.c.Key}},
		{"a federated repository's chart", func() {
			if _, err := w.deployTo(fed, "web-1.0.0.tgz", chartTgz("web", "1.0.0")); err != nil {
				t.Fatal(err)
			}
		}, []string{fed.Key}},
	} {
		w.queued(helm.KindIndex)
		c.write()
		if got := w.queued(helm.KindIndex); !slices.Equal(got, c.want) {
			t.Errorf("%s: queued %v, want %v", c.what, got, c.want)
		}
	}
}

// A remote repository's cache is indexed, but its index.yaml is the
// remote's, so nothing is queued, owed or derived for it.
func TestUnitRemoteCache(t *testing.T) {
	w := newWorld(t)
	r := repo.Config{
		Key:         "helm-remote",
		Class:       repo.Remote,
		PackageType: repo.Helm,
		URL:         "https://up.example",
	}
	repo.Defaults(&r)
	if err := w.st.Repos().Create(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: r, Cached: true, Path: "web-1.0.0.tgz"},
		Body:   bytes.NewReader(chartTgz("web", "1.0.0")),
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.index(r); got["web"]["1.0.0"].Meta.Path != "web-1.0.0.tgz" {
		t.Fatal(got)
	}
	if got := w.queued(helm.KindIndex); len(got) > 0 {
		t.Fatal(got)
	}
	if err := w.items.Reindex(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := w.queued(helm.KindIndex); len(got) > 0 {
		t.Fatal(got)
	}
	// A file stored in a local repository's own content is not the cache's.
	h := helm.Handler{}
	if err := h.Deployed(w.ctx, format.Env{}, w.c, types.Item{Cached: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleting(w.ctx, format.Env{}, w.c, types.Item{Cached: true}); err != nil {
		t.Fatal(err)
	}
}

// A read of the root index.yaml of a local or federated repository waits
// for its index.yaml work, and nothing else waits for anything.
func TestUnitOwed(t *testing.T) {
	h := helm.Handler{}
	if got := h.WorkKinds(); !slices.Equal(got, []string{helm.KindIndex}) {
		t.Fatal(got)
	}
	local := repo.Config{Key: "l", Class: repo.Local}
	fed := repo.Config{Key: "f", Class: repo.Federated}
	remote := repo.Config{Key: "r", Class: repo.Remote}
	for _, c := range []struct {
		c      repo.Config
		cached bool
		path   string
		want   []format.WorkRef
	}{
		{local, false, "index.yaml", []format.WorkRef{{Kind: helm.KindIndex, Key: "l"}}},
		{fed, false, "index.yaml", []format.WorkRef{{Kind: helm.KindIndex, Key: "f"}}},
		{local, false, "sub/index.yaml", nil},
		{local, false, "web-1.0.0.tgz", nil},
		{remote, true, "index.yaml", nil},
		{local, true, "index.yaml", nil},
	} {
		if got := h.Owed(c.c, c.cached, c.path); !cmp.Equal(got, c.want) {
			t.Errorf("%s %v %s: %v", c.c.Key, c.cached, c.path, got)
		}
	}
	if err := h.Work(
		context.Background(),
		format.Env{},
		types.WorkItem{Kind: "other", Key: "l"},
	); err != nil {
		t.Fatal(err)
	}
}

// Inspect reads chart-named content only, and Deployed uses what it read
// for the content it read.
func TestUnitInspect(t *testing.T) {
	w := newWorld(t)
	h := helm.Handler{}
	env := w.items.Env()
	it := w.deploy("web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	for _, c := range []struct {
		w     format.Write
		chart bool
		ok    bool
	}{
		{format.Write{Path: "web-1.0.0.tgz", Checksums: it.Actual}, true, true},
		{format.Write{Path: "web.tar.gz", Checksums: it.Actual}, true, true},
		{format.Write{Path: "web-1.0.0.tgz.prov", Checksums: it.Actual}, false, false},
		{format.Write{Path: "web-1.0.0.tgz"}, false, false},
	} {
		v, err := h.Inspect(w.ctx, env, w.c, c.w)
		if err != nil {
			t.Fatal(err)
		}
		ch, ok := helm.Inspected(v)
		if ok != c.ok || (ch != nil) != c.chart {
			t.Errorf("%s: %v %v", c.w.Path, ch, ok)
		}
	}
	// An inspection of this content is taken at its word.
	env.Inspection = helm.Inspection(it.Actual.SHA256, &helm.Chart{Name: "said", Version: "9.0.0"})
	if err := h.Deployed(w.ctx, env, w.c, it); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.index(w.c)["said"]["9.0.0"]; !ok {
		t.Fatal(w.index(w.c))
	}
}
