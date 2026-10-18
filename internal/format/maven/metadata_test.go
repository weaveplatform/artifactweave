package maven_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var update = flag.Bool("update", false, "rewrite the golden metadata files")

// metaWorld is a Maven repository behind the item service with the work
// queue: its handlers are registered, but no worker runs, so work is done
// only when a read asks for it, or by run.
type metaWorld struct {
	t     *testing.T
	ctx   context.Context
	db    store.Store
	blobs blob.Store
	c     repo.Config
	items *item.Service
	q     *work.Queue
	now   time.Time
}

func newMetaWorld(t *testing.T, tune func(*repo.Config)) *metaWorld {
	t.Helper()
	return newMetaWorldOn(t, inmem.New(), tune)
}

func newMetaWorldOn(t *testing.T, db store.Store, tune func(*repo.Config)) *metaWorld {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &metaWorld{
		t: t, ctx: context.Background(), db: db, blobs: blobs,
		now: time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC),
	}
	c := repo.Config{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven}
	repo.Defaults(&c)
	if tune != nil {
		tune(&c)
	}
	if err := db.Repos().Create(w.ctx, c); err != nil {
		t.Fatal(err)
	}
	w.c = c
	clock := func() time.Time { return w.now }
	w.q, err = work.New(db, work.Options{Now: clock, Poll: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	w.items = &item.Service{Store: db, Blobs: blobs, Now: clock, Work: w.q}
	if err := w.items.RegisterWork(w.q); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *metaWorld) deploy(p string, props types.Properties, body ...string) {
	w.t.Helper()
	// A POM must name its path's coordinates, or #278's checks refuse it.
	b := bodyOf(p)
	if len(body) > 0 {
		b = body[0]
	}
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: w.c, Path: p}, Body: strings.NewReader(b), Properties: props,
		By: "deployer",
	}); err != nil {
		w.t.Fatalf("deploy %s: %v", p, err)
	}
}

// deployAs deploys p as the repository would under another snapshot
// behaviour, which names it as that behaviour does, then goes back to its
// own.
func (w *metaWorld) deployAs(b repo.SnapshotVersionBehavior, p string) {
	w.t.Helper()
	own := w.c.SnapshotVersionBehavior
	w.c.SnapshotVersionBehavior = b
	defer func() { w.c.SnapshotVersionBehavior = own }()
	w.deploy(p, nil)
}

func (w *metaWorld) delete(p string) {
	w.t.Helper()
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.c, Path: p}, "deployer"); err != nil {
		w.t.Fatalf("delete %s: %v", p, err)
	}
}

// get reads p as a download does: resolved, which brings its metadata up
// to date, then looked up. ok is false when nothing is there.
func (w *metaWorld) get(p string) (string, types.Item, bool) {
	w.t.Helper()
	p, err := w.items.Resolve(w.ctx, w.c, format.Download{Path: p})
	if err != nil {
		w.t.Fatalf("resolve %s: %v", p, err)
	}
	it, err := w.db.Items().Get(w.ctx, w.c.Key, false, p)
	if errors.Is(err, store.ErrNotFound) {
		return "", types.Item{}, false
	}
	if err != nil {
		w.t.Fatal(err)
	}
	rc, _, err := w.blobs.Open(w.ctx, it.Actual.SHA256)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b), it, true
}

// queued counts the pending work of each kind.
func (w *metaWorld) queued() map[string]int64 {
	w.t.Helper()
	stats, err := w.db.WorkQueue().Stats(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, s := range stats {
		if n := s.Pending + s.Leased + s.Parked; n > 0 {
			out[s.Kind] = n
		}
	}
	return out
}

// golden compares got with testdata/name, or rewrites it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s:\n got:\n%s\nwant:\n%s", name, got, want)
	}
}

const pomTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>%s</groupId>
  <artifactId>%s</artifactId>
  <version>%s</version>
  <packaging>%s</packaging>%s
</project>
`

func pomOf(g, a, v, packaging, name string) string {
	if name != "" {
		name = "\n  <name>" + name + "</name>"
	}
	return fmt.Sprintf(pomTemplate, g, a, v, packaging, name)
}

// The artifact level lists the version folders that hold a POM in Mercury
// order, latest and version the highest, release the highest release, and
// a read straight after a deploy sees it.
func TestArtifactMetadata(t *testing.T) {
	w := newMetaWorld(t, nil)
	dir := "com/acme/app/"
	for _, v := range []string{"1.10", "1.0", "2.0-alpha-1", "1.9"} {
		w.deploy(dir+v+"/app-"+v+".pom", nil)
		w.deploy(dir+v+"/app-"+v+".jar", nil)
	}
	w.deploy(dir+"1.0-SNAPSHOT/app-1.0-20261017.090000-1.pom", nil)
	// A version folder without a POM is no version of the metadata.
	w.deploy(dir+"3.0/app-3.0.jar", nil)
	if got := w.queued(); got[maven.KindMetadata] != 1 || len(got) != 1 {
		t.Fatalf("queued %v; want one package's metadata", got)
	}
	body, it, ok := w.get(dir + "maven-metadata.xml")
	if !ok {
		t.Fatal("no artifact-level metadata")
	}
	golden(t, "artifact.xml", body)
	if it.ModifiedBy != maven.Deriver || it.Original != (types.Checksums{}) {
		t.Errorf("written by %q with client checksums %+v", it.ModifiedBy, it.Original)
	}
	if got := w.queued(); len(got) != 0 {
		t.Errorf("work left after the read: %v", got)
	}

	// Each deploy is seen by the next read.
	w.now = w.now.Add(time.Hour)
	w.deploy(dir+"2.0/app-2.0.pom", nil)
	body, _, _ = w.get(dir + "maven-metadata.xml")
	if !strings.Contains(body, "<release>2.0</release>") ||
		!strings.Contains(body, "<lastUpdated>20261017103000</lastUpdated>") {
		t.Errorf("after deploying 2.0:\n%s", body)
	}
}

// With only snapshots, the metadata has no release.
func TestArtifactMetadataSnapshotsOnly(t *testing.T) {
	w := newMetaWorld(t, nil)
	w.deploy("org/example/lib/1.0-SNAPSHOT/lib-1.0-20261017.090000-1.pom", nil)
	body, _, _ := w.get("org/example/lib/maven-metadata.xml")
	if strings.Contains(body, "<release>") ||
		!strings.Contains(body, "<latest>1.0-SNAPSHOT</latest>") {
		t.Errorf("%s", body)
	}
}

// A snapshot folder's metadata names its newest unique POM's build and the
// newest build of each extension and classifier.
func TestSnapshotMetadata(t *testing.T) {
	w := newMetaWorld(t, nil)
	dir := "com/acme/app/1.0-beta-SNAPSHOT/"
	b1, b2 := "app-1.0-beta-20261017.090000-1", "app-1.0-beta-20261017.100000-2"
	for _, p := range []string{
		b1 + ".pom", b1 + ".jar", b1 + "-sources.jar",
		b2 + ".jar", b2 + ".pom", "notes.txt",
	} {
		w.deploy(dir+p, nil)
	}
	// Neither notes.txt nor a -SNAPSHOT file is a unique build. Under
	// unique a -SNAPSHOT name is stored only from before the behaviour
	// changed: deployer keeps the name as sent.
	w.deployAs(repo.Deployer, dir+"app-1.0-beta-SNAPSHOT-tests.jar")
	body, _, ok := w.get(dir + "maven-metadata.xml")
	if !ok {
		t.Fatal("no snapshot metadata")
	}
	golden(t, "snapshot.xml", body)
	// The artifact level was written by the same run.
	if body, _, _ := w.get("com/acme/app/maven-metadata.xml"); !strings.Contains(body,
		"<version>1.0-beta-SNAPSHOT</version>") {
		t.Errorf("artifact level:\n%s", body)
	}
}

// Under non-unique, a snapshot folder's metadata names build 1 with no
// timestamp; under unique with no unique POM, it names none.
func TestSnapshotMetadataBuilds(t *testing.T) {
	w := newMetaWorld(t, func(c *repo.Config) { c.SnapshotVersionBehavior = repo.NonUnique })
	w.deploy("com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.pom", nil)
	body, _, _ := w.get("com/acme/app/1.0-SNAPSHOT/maven-metadata.xml")
	golden(t, "snapshot-nonunique.xml", body)

	w = newMetaWorld(t, nil)
	w.deployAs(repo.NonUnique, "com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.pom")
	body, _, _ = w.get("com/acme/app/1.0-SNAPSHOT/maven-metadata.xml")
	if !strings.Contains(body, "    <snapshot/>\n") || strings.Contains(body, "snapshotVersions") {
		t.Errorf("%s", body)
	}
}

// Metadata goes with the last POM it describes: a snapshot folder's
// whoever wrote it, the artifact level only when the server wrote it.
func TestMetadataRemoved(t *testing.T) {
	w := newMetaWorld(t, nil)
	dir := "com/acme/app/"
	w.deploy(dir+"1.0/app-1.0.pom", nil)
	w.deploy(dir+"1.0-SNAPSHOT/app-1.0-20261017.090000-1.pom", nil)
	w.deploy(dir+"1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar", nil)
	if _, _, ok := w.get(dir + "1.0-SNAPSHOT/maven-metadata.xml"); !ok {
		t.Fatal("no snapshot metadata")
	}
	w.delete(dir + "1.0-SNAPSHOT/app-1.0-20261017.090000-1.pom")
	if _, _, ok := w.get(dir + "1.0-SNAPSHOT/maven-metadata.xml"); ok {
		t.Error("the snapshot folder kept its metadata without a POM")
	}
	body, _, _ := w.get(dir + "maven-metadata.xml")
	if strings.Contains(body, "SNAPSHOT") {
		t.Errorf("the artifact level kept the snapshot:\n%s", body)
	}
	w.delete(dir + "1.0")
	if _, _, ok := w.get(dir + "maven-metadata.xml"); ok {
		t.Error("the artifact level outlived the last POM")
	}

	// The client's own artifact-level metadata stays while no POM can
	// replace it, and the server's replaces it once one can.
	client := "<metadata><versioning/></metadata>"
	w.deploy("org/example/lib/maven-metadata.xml", nil, client)
	if body, _, _ := w.get("org/example/lib/maven-metadata.xml"); body != client {
		t.Errorf("client metadata became:\n%s", body)
	}
	w.deploy("org/example/lib/2.0/lib-2.0.pom", nil)
	body, it, _ := w.get("org/example/lib/maven-metadata.xml")
	if !strings.Contains(body, "<version>2.0</version>") || it.ModifiedBy != maven.Deriver {
		t.Errorf("by %s:\n%s", it.ModifiedBy, body)
	}
}

// A group's plugin metadata lists one plugin per artifactId among the POMs
// marked as plugins, named by the highest version's POM.
func TestPluginMetadata(t *testing.T) {
	w := newMetaWorld(t, nil)
	plugin := types.Properties{maven.PluginProperty: {"true"}}
	g := "com.acme.plugins"
	dir := "com/acme/plugins/"
	suppress := true
	w.deploy(dir+"maven-foo-plugin/1.0/maven-foo-plugin-1.0.pom", plugin,
		pomOf(g, "maven-foo-plugin", "1.0", "maven-plugin", "Old Foo"))
	w.deploy(dir+"maven-foo-plugin/1.1/maven-foo-plugin-1.1.pom", plugin,
		pomOf(g, "maven-foo-plugin", "1.1", "maven-plugin", "Foo &amp; more"))
	w.deploy(dir+"bar-maven-plugin/2.0/bar-maven-plugin-2.0.pom", plugin,
		pomOf(g, "bar-maven-plugin", "2.0", "maven-plugin", ""))
	w.deploy(dir+"maven-plugin-plugin/3.0/maven-plugin-plugin-3.0.pom", plugin,
		pomOf(g, "maven-plugin-plugin", "3.0", "maven-plugin", "Plugin Plugin"))
	// Not marked, or not a POM, or not in the group's folder: no plugin.
	w.deploy(dir+"lib/1.0/lib-1.0.pom", nil, pomOf(g, "lib", "1.0", "jar", "Lib"))
	w.deploy(dir+"baz-plugin/1.0/baz-plugin-1.0.jar", plugin)
	w.deploy(dir+"nested/qux-plugin/1.0/qux-plugin-1.0.pom", plugin,
		pomOf(g+".nested", "qux-plugin", "1.0", "maven-plugin", ""))
	// A marked POM that does not parse names no plugin. Only with the POM
	// checks off can one be stored (#278).
	w.c.SuppressPomConsistencyChecks = &suppress
	w.deploy(dir+"broken-plugin/1.0/broken-plugin-1.0.pom", plugin, "not a POM")
	body, it, ok := w.get(dir + "maven-metadata.xml")
	if !ok {
		t.Fatal("no plugin metadata")
	}
	golden(t, "plugins.xml", body)

	// Unchanged plugins leave the file as it is.
	modified := it.Modified
	w.now = w.now.Add(time.Hour)
	w.deploy(dir+"bar-maven-plugin/2.0/bar-maven-plugin-2.0.pom", plugin,
		pomOf(g, "bar-maven-plugin", "2.0", "maven-plugin", ""))
	if _, it, _ := w.get(dir + "maven-metadata.xml"); !it.Modified.Equal(modified) {
		t.Error("unchanged plugin metadata was rewritten")
	}

	// Unmarking and deleting the plugins removes the file.
	for _, p := range []string{
		"maven-foo-plugin/1.0/maven-foo-plugin-1.0.pom",
		"maven-foo-plugin/1.1/maven-foo-plugin-1.1.pom",
		"maven-plugin-plugin/3.0/maven-plugin-plugin-3.0.pom",
	} {
		pom, err := w.db.Items().Get(w.ctx, w.c.Key, false, dir+p)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.items.Annotate(
			w.ctx,
			w.c,
			pom,
			nil,
			[]string{maven.PluginProperty},
		); err != nil {
			t.Fatal(err)
		}
	}
	if body, _, _ := w.get(dir + "maven-metadata.xml"); strings.Contains(body, "foo") {
		t.Errorf("unmarked plugins listed:\n%s", body)
	}
	w.delete(dir + "bar-maven-plugin")
	if _, _, ok := w.get(dir + "maven-metadata.xml"); ok {
		t.Error("plugin metadata outlived the last plugin")
	}
}

// Under deployer the client keeps every level of metadata: nothing is
// queued or derived.
func TestDeployerDerivesNothing(t *testing.T) {
	w := newMetaWorld(t, func(c *repo.Config) { c.SnapshotVersionBehavior = repo.Deployer })
	dir := "com/acme/app/"
	w.deploy(dir+"1.0/app-1.0.pom", types.Properties{maven.PluginProperty: {"true"}})
	w.deploy(dir+"1.0-SNAPSHOT/app-1.0-SNAPSHOT.pom", nil)
	w.deploy(dir+"maven-metadata.xml", nil, "client")
	w.delete(dir + "1.0/app-1.0.pom")
	if got := w.queued(); len(got) != 0 {
		t.Errorf("queued %v", got)
	}
	if body, _, _ := w.get(dir + "maven-metadata.xml"); body != "client" {
		t.Errorf("%q", body)
	}
	if _, _, ok := w.get(dir + "1.0-SNAPSHOT/maven-metadata.xml"); ok {
		t.Error("snapshot metadata derived under deployer")
	}
}

// Work asked for a repository that is gone, or no longer of the Maven
// group, or under deployer, owes nothing.
func TestWorkForChangedRepository(t *testing.T) {
	w := newMetaWorld(t, nil)
	w.deploy("com/acme/app/1.0/app-1.0.pom", nil)
	c := w.c
	c.SnapshotVersionBehavior = repo.Deployer
	if err := w.db.Repos().Update(w.ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := w.q.EnsureFresh(w.ctx, maven.KindMetadata, "maven-local/com.acme:app"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := w.get("com/acme/app/maven-metadata.xml"); ok {
		t.Error("metadata derived after the repository turned to deployer")
	}
	for _, key := range []string{"gone/com.acme:app", "no-slash"} {
		if err := w.db.WorkQueue().Enqueue(w.ctx, maven.KindMetadata, key, nil, w.now); err != nil {
			t.Fatal(err)
		}
		if err := w.q.EnsureFresh(w.ctx, maven.KindMetadata, key); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
}

// Concurrent deploys of one package, with the workers running, lose no
// version: work asked for during a run runs again.
func TestConcurrentDeploys(t *testing.T) {
	w := newMetaWorld(t, nil)
	ctx, cancel := context.WithCancel(w.ctx)
	done := make(chan struct{})
	go func() { _ = w.q.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			v := fmt.Sprintf("1.%d", i)
			if _, err := w.items.Deploy(w.ctx, item.Deploy{
				Target: item.Target{Repo: w.c, Path: "com/acme/app/" + v + "/app-" + v + ".pom"},
				Body:   strings.NewReader(bodyOf("com/acme/app/" + v + "/app-" + v + ".pom")),
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	wctx, wcancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer wcancel()
	if err := w.q.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	it, err := w.db.Items().Get(w.ctx, w.c.Key, false, "com/acme/app/maven-metadata.xml")
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := w.blobs.Open(w.ctx, it.Actual.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	for i := range 12 {
		if !bytes.Contains(b, fmt.Appendf(nil, "<version>1.%d</version>", i)) {
			t.Errorf("1.%d missing:\n%s", i, b)
		}
	}
}

// A download of metadata waits for the work that may write it.
func TestOwed(t *testing.T) {
	h := maven.Handler{}
	local := repo.Config{Key: "m", Class: repo.Local}
	pkg := func(name string) format.WorkRef { return format.WorkRef{Kind: maven.KindMetadata, Key: "m/" + name} }
	plugins := func(g string) format.WorkRef { return format.WorkRef{Kind: maven.KindPlugins, Key: "m/" + g} }
	for _, tc := range []struct {
		c      repo.Config
		cached bool
		path   string
		want   []format.WorkRef
	}{
		{local, false, "com/acme/app/1.0-SNAPSHOT/maven-metadata.xml", []format.WorkRef{pkg("com.acme:app")}},
		{local, false, "com/acme/app/maven-metadata.xml", []format.WorkRef{pkg("com.acme:app"), plugins("com.acme.app")}},
		{local, false, "acme/maven-metadata.xml", []format.WorkRef{plugins("acme")}},
		{local, false, "app/1.0-SNAPSHOT/maven-metadata.xml", nil},
		{local, false, "maven-metadata.xml", nil},
		{local, false, "com/acme/app/1.0/app-1.0.jar", nil},
		{local, true, "com/acme/app/maven-metadata.xml", nil},
		{repo.Config{Key: "r", Class: repo.Remote}, false, "com/acme/app/maven-metadata.xml", nil},
		{
			repo.Config{Key: "m", Class: repo.Local, SnapshotVersionBehavior: repo.Deployer},
			false, "com/acme/app/maven-metadata.xml", nil,
		},
	} {
		if got := h.Owed(tc.c, tc.cached, tc.path); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v; want %v", tc.path, got, tc.want)
		}
	}
	if !slices.Equal(h.WorkKinds(), []string{maven.KindMetadata, maven.KindPlugins}) {
		t.Errorf("kinds %v", h.WorkKinds())
	}
}

// Metadata of the Maven group's other layouts follows their folders: a
// Gradle repository's package folder, and none for Maven 1, whose layout
// files every version of a group together.
func TestMetadataLayouts(t *testing.T) {
	w := newMetaWorld(t, func(c *repo.Config) {
		c.PackageType, c.RepoLayoutRef = repo.Gradle, repo.LayoutGradle
		// Gradle's default (pkg/repo/defaults.go): its paths are no Maven
		// paths, which the POM checks would refuse.
		suppress := true
		c.SuppressPomConsistencyChecks = &suppress
	})
	w.deploy("org.example/app/1.0/app-1.0.pom", nil)
	if body, _, _ := w.get("org.example/app/maven-metadata.xml"); !strings.Contains(body,
		"<groupId>org.example</groupId>") {
		t.Errorf("gradle:\n%s", body)
	}

	w = newMetaWorld(t, func(c *repo.Config) { c.RepoLayoutRef = "maven-1-default" })
	w.deploy("org.example/poms/app-1.0.pom", nil)
	if got := w.queued(); got[maven.KindMetadata] != 1 {
		t.Fatalf("queued %v", got)
	}
	if err := w.q.EnsureFresh(
		w.ctx,
		maven.KindMetadata,
		"maven-local/org.example:app",
	); err != nil {
		t.Fatal(err)
	}
	hits, err := w.db.Items().Search(w.ctx, metadataFiles(w.c), aql.Options{})
	if err != nil || len(hits) != 0 {
		t.Errorf("maven-1 metadata %v: %v", hits, err)
	}

	// A layout that is not defined reads no path, so there is nothing to
	// derive.
	w = newMetaWorld(t, func(c *repo.Config) { c.RepoLayoutRef = "absent" })
	w.deploy("a/b/1.0/b-1.0.pom", nil)
	if err := w.db.WorkQueue().
		Enqueue(w.ctx, maven.KindMetadata, "maven-local/a:b", nil, w.now); err != nil {
		t.Fatal(err)
	}
	if err := w.q.EnsureFresh(w.ctx, maven.KindMetadata, "maven-local/a:b"); err != nil {
		t.Fatal(err)
	}
	if hits, _ := w.db.Items().Search(w.ctx, metadataFiles(w.c), aql.Options{}); len(hits) != 0 {
		t.Errorf("metadata %v", hits)
	}
}

func metadataFiles(c repo.Config) *aql.Query {
	return &aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: c.Key}}},
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: maven.MetadataFile},
	}}
}

// A property change queues plugin metadata only for a POM whose plugin
// mark changed; removing a POM that was never marked queues none.
func TestPluginWorkQueued(t *testing.T) {
	w := newMetaWorld(t, nil)
	w.deploy("com/acme/x/1.0/x-1.0.pom", nil)
	w.deploy("com/acme/x/1.0/x-1.0.jar", types.Properties{maven.PluginProperty: {"true"}})
	pom, err := w.db.Items().Get(w.ctx, w.c.Key, false, "com/acme/x/1.0/x-1.0.pom")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.items.Annotate(w.ctx, w.c, pom, types.Properties{"other": {"1"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.items.MakeFolder(w.ctx, item.Target{Repo: w.c, Path: "com/acme/x"},
		types.Properties{maven.PluginProperty: {"true"}}, "deployer"); err != nil {
		t.Fatal(err)
	}
	w.delete("com/acme/x/1.0/x-1.0.pom")
	if got := w.queued(); got[maven.KindPlugins] != 0 {
		t.Errorf("queued %v", got)
	}
}

// Every deploy and delete in a group recalculates its plugin metadata, as
// Artifactory has since 7.10.1: a package's metadata run rebuilds its
// group's plugins, and a marked POM's redeploy queues them for its readers.
func TestPluginMetadataOnEveryWrite(t *testing.T) {
	w := newMetaWorld(t, nil)
	g, dir := "com.acme.plugins", "com/acme/plugins/"
	pom := dir + "maven-foo-plugin/1.0/maven-foo-plugin-1.0.pom"
	// Deployed as a jar, which #278 does not mark, then marked behind the
	// format's back, as an import might leave it.
	w.deploy(pom, nil, pomOf(g, "maven-foo-plugin", "1.0", "jar", "Foo"))
	it, err := w.db.Items().Get(w.ctx, w.c.Key, false, pom)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Items().PatchProperties(w.ctx, it.ID,
		types.Properties{maven.PluginProperty: {"true"}}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := w.queued(); got[maven.KindPlugins] != 0 {
		t.Fatalf("queued %v", got)
	}
	// Any write to the group's packages brings the plugin in.
	w.deploy(dir+"lib/1.0/lib-1.0.jar", nil)
	if _, _, ok := w.get(dir + "lib/maven-metadata.xml"); ok {
		t.Fatal("a package without a POM got metadata")
	}
	if body, _, _ := w.get(
		dir + "maven-metadata.xml",
	); !strings.Contains(
		body,
		"<name>Foo</name>",
	) {
		t.Fatalf("after a write to the group:\n%s", body)
	}
	// A marked POM redeployed without its mark in the request is read
	// fresh by the next read of the group's metadata.
	w.deploy(pom, nil, pomOf(g, "maven-foo-plugin", "1.0", "maven-plugin", "Foo Two"))
	if body, _, _ := w.get(
		dir + "maven-metadata.xml",
	); !strings.Contains(
		body,
		"<name>Foo Two</name>",
	) {
		t.Errorf("after the redeploy:\n%s", body)
	}
}
