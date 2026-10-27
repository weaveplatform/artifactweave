package maven_test

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const snapDir = "com/acme/app/1.0-SNAPSHOT/"

// put deploys p with props and returns the path it was stored at.
func (w *metaWorld) put(p string, props types.Properties) string {
	w.t.Helper()
	it, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: w.c, Path: p}, Body: strings.NewReader(bodyOf(p)),
		Properties: props, By: "deployer",
	})
	if err != nil {
		w.t.Fatalf("deploy %s: %v", p, err)
	}
	return it.FullPath()
}

// sidecar uploads the right SHA-1 of the file a -SNAPSHOT sidecar at p
// names, and returns the path it was recorded on.
func (w *metaWorld) sidecar(p, want string) string {
	w.t.Helper()
	it, err := w.db.Items().Get(w.ctx, w.c.Key, false, want)
	if err != nil {
		w.t.Fatalf("%s: %v", want, err)
	}
	got, err := w.items.RecordSidecar(w.ctx, item.Target{Repo: w.c, Path: p}, item.SHA1,
		it.Actual.SHA1, 40)
	if err != nil {
		w.t.Fatalf("sidecar of %s: %v", p, err)
	}
	return got.FullPath()
}

// stamped is a build.timestamp matrix parameter for t.
func stamped(t time.Time) types.Properties {
	return types.Properties{"build.timestamp": {strconv.FormatInt(t.UnixMilli(), 10)}}
}

func expect(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: stored at %s, want %s", what, got, want)
	}
}

var buildNumber = regexp.MustCompile(`<buildNumber>([0-9]+)</buildNumber>`)

// Without a build.timestamp, the server numbers -SNAPSHOT files as
// Artifactory does: a main artifact starts a build, its POM and classifier
// artifacts join it, a sidecar names the newest build of its file, and a
// build's files share the timestamp of its first. Two builds in the order
// Maven 2 and curl scripts deploy give buildNumber 2 and snapshotVersions of
// build 2 (the acceptance of #280).
func TestUnitUniqueSnapshotServerNumbers(t *testing.T) {
	w := newMetaWorld(t, nil)
	b1, b2 := snapDir+"app-1.0-20261017.093000-1", snapDir+"app-1.0-20261017.103000-2"
	for i, b := range []string{b1, b2} {
		w.now = time.Date(2026, 10, 17, 9+i, 30, 0, 0, time.UTC)
		expect(t, "jar", w.put(snapDir+"app-1.0-SNAPSHOT.jar", nil), b+".jar")
		expect(t, "jar sidecar", w.sidecar(snapDir+"app-1.0-SNAPSHOT.jar", b+".jar"), b+".jar")
		// The timestamp is the build's, not the clock's.
		w.now = w.now.Add(time.Minute)
		expect(t, "pom", w.put(snapDir+"app-1.0-SNAPSHOT.pom", nil), b+".pom")
		expect(t, "pom sidecar", w.sidecar(snapDir+"app-1.0-SNAPSHOT.pom", b+".pom"), b+".pom")
		expect(t, "sources", w.put(snapDir+"app-1.0-SNAPSHOT-sources.jar", nil), b+"-sources.jar")
	}
	body, _, ok := w.get(snapDir + maven.MetadataFile)
	if !ok {
		t.Fatal("no snapshot metadata")
	}
	for _, want := range []string{
		"<timestamp>20261017.103000</timestamp>",
		"<buildNumber>2</buildNumber>",
		"<extension>jar</extension>\n        <value>1.0-20261017.103000-2</value>",
		"<extension>pom</extension>\n        <value>1.0-20261017.103000-2</value>",
		"<classifier>sources</classifier>\n        <extension>jar</extension>\n" +
			"        <value>1.0-20261017.103000-2</value>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in\n%s", want, body)
		}
	}
	if strings.Contains(body, "093000-1") {
		t.Errorf("build 1 is still named:\n%s", body)
	}
}

// A POM with no main artifact before it starts a build when its build
// already has a POM, as a parent POM's deploys do. A POM deployed before
// its main artifact keeps build 1 to itself, as in Artifactory: the next
// main artifact starts build 2, which is why the JFrog CLI sends a
// build.timestamp.
func TestUnitUniqueSnapshotPOMs(t *testing.T) {
	w := newMetaWorld(t, nil)
	parent := "com/acme/parent/1.0-SNAPSHOT/"
	expect(t, "parent 1", w.put(parent+"parent-1.0-SNAPSHOT.pom", nil),
		parent+"parent-1.0-20261017.093000-1.pom")
	w.now = w.now.Add(time.Hour)
	expect(t, "parent 2", w.put(parent+"parent-1.0-SNAPSHOT.pom", nil),
		parent+"parent-1.0-20261017.103000-2.pom")

	expect(t, "pom first", w.put(snapDir+"app-1.0-SNAPSHOT.pom", nil),
		snapDir+"app-1.0-20261017.103000-1.pom")
	w.now = w.now.Add(time.Minute)
	expect(t, "its jar", w.put(snapDir+"app-1.0-SNAPSHOT.jar", nil),
		snapDir+"app-1.0-20261017.103100-2.jar")
}

// The JFrog CLI and its Maven extractor send one build.timestamp with every
// file of a build, which names the build whatever order the files come in:
// the build that already has that timestamp, or the next number. A value
// that is not a number is ignored.
func TestUnitUniqueSnapshotClientTimestamp(t *testing.T) {
	w := newMetaWorld(t, nil)
	t1 := time.Date(2026, 10, 17, 8, 15, 42, 123e6, time.UTC)
	t2 := t1.Add(25 * time.Minute)
	b1, b2 := snapDir+"app-1.0-20261017.081542-1", snapDir+"app-1.0-20261017.084042-2"
	// The POM first, then the artifacts, as no server-numbered order could.
	for _, f := range []string{".pom", ".jar", "-sources.jar"} {
		expect(t, "build 1"+f, w.put(snapDir+"app-1.0-SNAPSHOT"+f, stamped(t1)), b1+f)
	}
	for _, f := range []string{"-sources.jar", ".jar", ".pom"} {
		expect(t, "build 2"+f, w.put(snapDir+"app-1.0-SNAPSHOT"+f, stamped(t2)), b2+f)
	}
	// A file sent again with build 1's timestamp rejoins build 1.
	expect(t, "redeploy", w.put(snapDir+"app-1.0-SNAPSHOT-javadoc.jar", stamped(t1)),
		b1+"-javadoc.jar")
	// The extractor first tries a checksum deploy of content the server may
	// hold; it lands where an upload would.
	src, err := w.db.Items().Get(w.ctx, w.c.Key, false, b2+".jar")
	if err != nil {
		t.Fatal(err)
	}
	it, err := w.items.DeployByChecksum(w.ctx, item.Deploy{
		Target:   item.Target{Repo: w.c, Path: snapDir + "app-1.0-SNAPSHOT-tests.jar"},
		Declared: types.Checksums{SHA1: src.Actual.SHA1}, Properties: stamped(t2), By: "deployer",
	})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, "checksum deploy", it.FullPath(), b2+"-tests.jar")

	expect(t, "not a number", w.put(snapDir+"app-1.0-SNAPSHOT.jar",
		types.Properties{"build.timestamp": {"soon"}}),
		snapDir+"app-1.0-20261017.093000-3.jar")
}

// Maven 3 names its unique builds itself, from the snapshot metadata it
// reads before it deploys, and uploads metadata the server drops or
// replaces. Its names are kept, and two deploys in its order give
// buildNumber 2.
func TestUnitUniqueSnapshotMaven3(t *testing.T) {
	w := newMetaWorld(t, nil)
	for i := range 2 {
		w.now = time.Date(2026, 10, 17, 9+i, 30, 0, 0, time.UTC)
		n := 1
		if body, _, ok := w.get(snapDir + maven.MetadataFile); ok {
			m := buildNumber.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("build %d: no buildNumber in\n%s", i+1, body)
			}
			last, _ := strconv.Atoi(m[1])
			n = last + 1
		}
		b := snapDir + "app-1.0-" + w.now.Format("20060102.150405") + "-" + strconv.Itoa(n)
		for _, f := range []string{".jar", ".pom", "-sources.jar"} {
			expect(t, "maven 3"+f, w.put(b+f, nil), b+f)
			expect(t, "maven 3 sidecar"+f, w.sidecar(b+f, b+f), b+f)
		}
		_, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: w.c, Path: snapDir + maven.MetadataFile},
			Body:   strings.NewReader("<metadata/>"),
		})
		var accepted *format.AcceptedError
		if !errors.As(err, &accepted) || accepted.Status != http.StatusAccepted {
			t.Fatalf("snapshot metadata: %v", err)
		}
		w.deploy("com/acme/app/"+maven.MetadataFile, nil, "<metadata/>")
	}
	body, _, _ := w.get(snapDir + maven.MetadataFile)
	if !strings.Contains(body, "<buildNumber>2</buildNumber>") ||
		!strings.Contains(body, "<value>1.0-20261017.103000-2</value>") {
		t.Errorf("after two deploys:\n%s", body)
	}
}

// A -SNAPSHOT sidecar is recorded on the newest build of its own file, and
// one with no such build answers 404 for the path the client sent.
func TestUnitUniqueSnapshotSidecars(t *testing.T) {
	w := newMetaWorld(t, nil)
	b2 := snapDir + "app-1.0-20261017.093000-2"
	for _, p := range []string{
		"app-1.0-20261017.093000-2.jar", "app-1.0-20261017.083000-1.jar",
		"app-1.0-20261017.083000-1-sources.jar",
	} {
		w.put(snapDir+p, nil)
	}
	expect(t, "jar", w.sidecar(snapDir+"app-1.0-SNAPSHOT.jar", b2+".jar"), b2+".jar")
	expect(t, "sources", w.sidecar(snapDir+"app-1.0-SNAPSHOT-sources.jar",
		snapDir+"app-1.0-20261017.083000-1-sources.jar"),
		snapDir+"app-1.0-20261017.083000-1-sources.jar")
	_, err := w.items.RecordSidecar(w.ctx,
		item.Target{Repo: w.c, Path: snapDir + "app-1.0-SNAPSHOT-javadoc.jar"},
		item.SHA1, strings.Repeat("0", 40), 40)
	var side *item.SidecarError
	if !errors.As(err, &side) || !errors.Is(err, store.ErrNotFound) || side.Message !=
		"Target file to set checksum on doesn't exist: maven-local:"+
			snapDir+"app-1.0-SNAPSHOT-javadoc.jar" {
		t.Fatalf("no build: %v", err)
	}
}

// Non-unique stores a unique build of the folder's version as -SNAPSHOT,
// so each build replaces the last, and its sidecars follow.
func TestUnitNonUniqueSnapshots(t *testing.T) {
	w := newMetaWorld(t, func(c *repo.Config) { c.SnapshotVersionBehavior = repo.NonUnique })
	for _, b := range []string{"app-1.0-20261017.093000-1", "app-1.0-20261017.103000-2"} {
		expect(t, b, w.put(snapDir+b+".jar", nil), snapDir+"app-1.0-SNAPSHOT.jar")
		expect(t, b+" sources", w.put(snapDir+b+"-sources.jar", nil),
			snapDir+"app-1.0-SNAPSHOT-sources.jar")
		expect(t, b+" sidecar", w.sidecar(snapDir+b+".jar", snapDir+"app-1.0-SNAPSHOT.jar"),
			snapDir+"app-1.0-SNAPSHOT.jar")
	}
	// A classifier that looks like a build makes no build of the folder's
	// version.
	look := snapDir + "app-1.0-SNAPSHOT-20261017.093000-1.jar"
	expect(t, "classifier", w.put(look, nil), look)
}

// Files that are no -SNAPSHOT file of their folder's version, and every
// file of a repository that does not rename, land as sent.
func TestUnitSnapshotNamesKept(t *testing.T) {
	w := newMetaWorld(t, nil)
	for _, p := range []string{
		"com/acme/app/1.0/app-1.0.jar",
		snapDir + "app-1.0-20261017.080000-7.jar",
		snapDir + "notes.txt",
		snapDir + "app-1.0.jar",
	} {
		expect(t, p, w.put(p, nil), p)
	}
	for name, tune := range map[string]func(*repo.Config){
		"deployer": func(c *repo.Config) { c.SnapshotVersionBehavior = repo.Deployer },
		"gradle layout": func(c *repo.Config) {
			c.PackageType, c.RepoLayoutRef = repo.Gradle, repo.LayoutGradle
		},
		"undefined layout": func(c *repo.Config) { c.RepoLayoutRef = "no-such-layout" },
	} {
		w := newMetaWorld(t, tune)
		p := snapDir + "app-1.0-SNAPSHOT.jar"
		expect(t, name, w.put(p, nil), p)
	}

	// A remote repository's cache keeps what its upstream names.
	h := maven.Handler{}
	c := repo.Config{Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven}
	repo.Defaults(&c)
	got, err := h.Admit(context.Background(), format.Env{Layouts: layouts{}}, c, format.Write{
		Op: format.OpDeploy, Path: snapDir + "app-1.0-SNAPSHOT.jar", Cached: true,
	})
	if err != nil || got.Path != snapDir+"app-1.0-SNAPSHOT.jar" {
		t.Fatalf("cache: %+v %v", got, err)
	}
}

// Replacing a file needs the right to delete it at the path it is stored
// at, which a renamed deploy is asked about.
func TestUnitUniqueSnapshotOverwrite(t *testing.T) {
	w := newMetaWorld(t, nil)
	w.put(snapDir+"app-1.0-SNAPSHOT.jar", nil)
	sources := snapDir + "app-1.0-SNAPSHOT-sources.jar"
	stored := w.put(sources, nil)
	var asked []string
	deploy := func(may bool) error {
		_, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: w.c, Path: sources}, Body: strings.NewReader("again"),
			By: "deployer", CanOverwritePath: func(p string) bool {
				asked = append(asked, p)
				return may
			},
		})
		return err
	}
	// The refusal names the file the deploy would replace, not the path
	// sent.
	var denied *item.OverwriteDeniedError
	if err := deploy(false); !errors.As(err, &denied) || denied.Path != stored ||
		denied.Repo != w.c.Key || denied.User != "deployer" {
		t.Fatalf("without the right: %v", err)
	}
	if err := deploy(true); err != nil {
		t.Fatalf("with the right: %v", err)
	}
	if len(asked) != 2 || asked[0] != stored || asked[1] != stored {
		t.Errorf("asked about %v, want %s", asked, stored)
	}
}

// A failure to list the snapshot folder fails the deploy or the checksum
// upload, which rolls its transaction back.
func TestUnitUniqueSnapshotSearchFailure(t *testing.T) {
	w := newWorld(t, nil)
	env := format.Env{Store: faulty{w.db, map[string]bool{"Search": true}}, Layouts: layouts{}}
	for _, op := range []format.Op{format.OpDeploy, format.OpChecksum} {
		if _, err := (maven.Handler{}).Admit(w.ctx, env, w.c, format.Write{
			Op: op, Path: snapDir + "app-1.0-SNAPSHOT.jar",
		}); !errors.Is(err, errIndex) {
			t.Errorf("op %v: %v", op, err)
		}
	}
}
