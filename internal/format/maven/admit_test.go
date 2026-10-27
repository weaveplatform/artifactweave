package maven_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// try deploys body at p and returns the error.
func (w *world) try(p, body string) error {
	w.t.Helper()
	_, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: w.c, Path: p}, Body: strings.NewReader(body),
	})
	return err
}

// stored reports whether p is an item of the repository.
func (w *world) stored(p string) bool {
	w.t.Helper()
	_, err := w.db.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		w.t.Fatal(err)
	}
	return err == nil
}

// refusal is err's status and message as a format refused with them.
func refusal(err error) (int, string) {
	var r *format.RejectionError
	if errors.As(err, &r) {
		return r.Status, r.Message
	}
	var a *format.AcceptedError
	if errors.As(err, &a) {
		return a.Status, ""
	}
	return 0, ""
}

func off() *bool { b := false; return &b }

func on() *bool { b := true; return &b }

// A deployed POM must name the coordinates of its path, unless the
// repository suppresses the checks; the messages are Artifactory's.
func TestUnitPOMChecks(t *testing.T) {
	const tail = "Please verify your POM content for correctness and make sure the source path " +
		"is a valid Maven repository root path."
	for _, tc := range []struct {
		name, path, body string
		// expected is the message a refusal starts with, or "" for none.
		expected string
	}{
		{"matching", "com/acme/app/1.0/app-1.0.pom", pomXML("com.acme", "app", "1.0", ""), ""},
		{
			"another group", "com/acme/app/1.0/app-1.0.pom", pomXML("com.other", "app", "1.0", ""),
			"The target deployment path 'com/acme/app/1.0/app-1.0.pom' does not match the POM's " +
				"expected path prefix 'com/other/app/1.0'. " + tail,
		},
		{
			"another version", "com/acme/app/1.0/app-1.0.pom", pomXML("com.acme", "app", "2.0", ""),
			"The target deployment path 'com/acme/app/1.0/app-1.0.pom' does not match the POM's " +
				"expected path prefix 'com/acme/app/2.0'.",
		},
		{"case", "Com/Acme/App/1.0/App-1.0.pom", pomXML("com.acme", "app", "1.0", ""), ""},
		{
			"from the parent", "com/acme/app/1.0/app-1.0.pom",
			`<project><parent><groupId>com.acme</groupId><version>1.0</version></parent>` +
				`<artifactId>app</artifactId></project>`, "",
		},
		{"no group", "com/acme/app/1.0/app-1.0.pom", `<project><artifactId>x</artifactId></project>`, ""},
		{
			"no version", "com/acme/app/1.0/app-1.0.pom",
			`<project><groupId>com.acme</groupId><artifactId>app</artifactId></project>`,
			"The Pom version of 'com/acme/app/1.0/app-1.0.pom' does not exists. Please verify " +
				"your POM content for correctness",
		},
		{"a property", "com/acme/app/1.0/app-1.0.pom", pomXML("com.acme", "app", "${revision}", ""), ""},
		{"not xml", "com/acme/app/1.0/app-1.0.pom", "not a pom", "Failed to read POM for 'com/acme/app/1.0/app-1.0.pom': "},
		{"not a project", "com/acme/app/1.0/app-1.0.pom", "<settings/>", "Failed to read POM for "},
		{
			"unique build", "com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.pom",
			pomXML("com.acme", "app", "1.0-SNAPSHOT", ""), "",
		},
		{
			// A snapshot path whose POM names a timestamped version is
			// compared up to the artifact's folder.
			"timestamped", "com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.pom",
			pomXML("com.acme", "app", "1.0-20261017.090000-1", ""), "",
		},
		{
			"snapshot pom in a release folder", "com/acme/app/1.0/app-1.0.pom",
			pomXML("com.acme", "app", "1.0-SNAPSHOT", ""),
			"The target deployment path 'com/acme/app/1.0/app-1.0.pom' does not match the POM's " +
				"expected path prefix 'com/acme/app/1.0-SNAPSHOT'.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, nil)
			err := w.try(tc.path, tc.body)
			status, msg := refusal(err)
			switch {
			case tc.expected == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.expected != "" && (status != http.StatusConflict || !strings.HasPrefix(msg, tc.expected)):
				t.Fatalf("got %d %q, want 409 %q", status, msg, tc.expected)
			}
			// Suppressed, the POM is kept whatever it says.
			w = newWorld(t, func(c *repo.Config) { c.SuppressPomConsistencyChecks = on() })
			if err := w.try(tc.path, tc.body); err != nil {
				t.Fatalf("suppressed: %v", err)
			}
		})
	}
}

// Gradle, Ivy and SBT repositories suppress the checks unless told
// otherwise; a Maven one does not.
func TestUnitPOMCheckDefaults(t *testing.T) {
	bad := pomXML("com.other", "app", "1.0", "")
	for pt, refused := range map[repo.PackageType]bool{
		repo.Maven: true, repo.Gradle: false, repo.Ivy: false, repo.SBT: false,
	} {
		w := newWorld(t, func(c *repo.Config) {
			*c = repo.Config{Key: c.Key, Class: repo.Local, PackageType: pt}
			repo.Defaults(c)
		})
		if err := w.try("com/acme/app/1.0/app-1.0.pom", bad); (err != nil) != refused {
			t.Errorf("%s: %v", pt, err)
		}
	}
}

// A POM with maven-plugin packaging is marked as a plugin, checked or not.
func TestUnitPluginProperty(t *testing.T) {
	plugin := "<packaging>maven-plugin</packaging><name>Acme Plugin</name>"
	for _, suppress := range []bool{false, true} {
		w := newWorld(t, func(c *repo.Config) {
			if suppress {
				c.SuppressPomConsistencyChecks = on()
			}
		})
		p := "com/acme/acme-maven-plugin/1.0/acme-maven-plugin-1.0.pom"
		if err := w.try(p, pomXML("com.acme", "acme-maven-plugin", "1.0", plugin)); err != nil {
			t.Fatal(err)
		}
		it, err := w.db.Items().Get(w.ctx, w.c.Key, false, p)
		if err != nil {
			t.Fatal(err)
		}
		props, err := w.db.Items().Properties(w.ctx, it.ID)
		if err != nil || len(props[maven.PluginProperty]) != 1 ||
			props[maven.PluginProperty][0] != "true" {
			t.Fatalf("suppress %v: properties %v %v", suppress, props, err)
		}
	}
	w := newWorld(t, nil)
	p := "com/acme/app/1.0/app-1.0.pom"
	if err := w.try(p, pomXML("com.acme", "app", "1.0", "<packaging>jar</packaging>")); err != nil {
		t.Fatal(err)
	}
	it, _ := w.db.Items().Get(w.ctx, w.c.Key, false, p)
	if props, _ := w.db.Items().Properties(w.ctx, it.ID); len(props[maven.PluginProperty]) != 0 {
		t.Fatalf("a jar's POM was marked: %v", props)
	}
}

// handleReleases and handleSnapshots refuse deploys and downloads of the
// other kind with SnapshotPolicyException's 409, after a blacked-out
// repository and before the include and exclude patterns, as Artifactory
// checks them; metadata, checksums and paths the layout does not read are
// exempt.
func TestUnitReleaseSnapshotPolicy(t *testing.T) {
	const (
		release  = "com/acme/app/1.0/app-1.0.jar"
		snapshot = "com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar"
	)
	policy := func(p string) string {
		return "The repository 'maven-local' rejected the resolution of an artifact 'maven-local:" +
			p + "' due to conflict in the snapshot release handling policy."
	}
	for _, tc := range []struct {
		name             string
		tune             func(*repo.Config)
		refused, allowed string
	}{
		{"no snapshots", func(c *repo.Config) { c.HandleSnapshots = off() }, snapshot, release},
		{"no releases", func(c *repo.Config) { c.HandleReleases = off() }, release, snapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, tc.tune)
			if s, m := refusal(w.try(tc.refused, "x")); s != http.StatusConflict ||
				m != policy(tc.refused) {
				t.Fatalf("deploy: %d %q", s, m)
			}
			for _, p := range []string{
				tc.allowed, "com/acme/app/maven-metadata.xml", "com/acme/app/notes.txt",
			} {
				if err := w.try(p, "x"); err != nil {
					t.Fatalf("%s: %v", p, err)
				}
			}
			h := maven.Handler{}
			env := format.Env{Store: w.db, Layouts: layouts{}}
			if _, err := h.Resolve(w.ctx, env, w.c, format.Download{Path: tc.refused}); err == nil {
				t.Fatal("the download was not refused")
			} else if s, m := refusal(err); s != http.StatusConflict || m != policy(tc.refused) {
				t.Fatalf("download: %d %q", s, m)
			}
			for _, p := range []string{tc.allowed, tc.refused + ".sha1", "readme.txt"} {
				if got, err := h.Resolve(w.ctx, env, w.c, format.Download{Path: p}); err != nil ||
					got != p {
					t.Fatalf("download %s: %q %v", p, got, err)
				}
			}
			broken := format.Env{Store: w.db, Layouts: layouts{fail: true}}
			if _, err := h.Resolve(
				w.ctx,
				broken,
				w.c,
				format.Download{Path: tc.refused},
			); !errors.Is(
				err,
				errLayout,
			) {
				t.Fatalf("broken layout: %v", err)
			}
			// A copy lands under the same policy.
			src := w.repo("maven-src", nil)
			if _, err := w.items.Deploy(w.ctx, item.Deploy{
				Target: item.Target{Repo: src, Path: tc.refused}, Body: strings.NewReader("x"),
			}); err != nil {
				t.Fatal(err)
			}
			res, err := w.items.Transfer(w.ctx, item.Transfer{
				From: item.Target{Repo: src, Path: tc.refused},
				To:   item.Target{Repo: w.c, Path: tc.refused},
			})
			if err != nil || res.Failed != 1 ||
				!strings.HasSuffix(res.Messages[0].Text, policy(tc.refused)) {
				t.Fatalf("copy: %+v %v", res, err)
			}
		})
	}
	// Blacked out answers first, and the policy before the patterns.
	w := newWorld(t, func(c *repo.Config) {
		c.HandleSnapshots, c.BlackedOut = off(), on()
	})
	if s, _ := refusal(w.try(snapshot, "x")); s != http.StatusNotFound {
		t.Fatalf("blacked out: %d", s)
	}
	excludes := "**/*SNAPSHOT*/**"
	w = newWorld(t, func(c *repo.Config) {
		c.HandleSnapshots, c.ExcludesPattern = off(), &excludes
	})
	if s, m := refusal(w.try(snapshot, "x")); s != http.StatusConflict || m != policy(snapshot) {
		t.Fatalf("excluded too: %d %q", s, m)
	}
	// Both handled, a download never reads the layout.
	w = newWorld(t, nil)
	broken := format.Env{Store: w.db, Layouts: layouts{fail: true}}
	if _, err := (maven.Handler{}).Resolve(
		w.ctx, broken, w.c, format.Download{Path: snapshot},
	); err != nil {
		t.Fatal(err)
	}
}

// The client's snapshot-level maven-metadata.xml is read and dropped with
// 202 under the Maven layout, except under deployer; artifact-level
// metadata and other layouts store it.
func TestUnitUploadedMetadata(t *testing.T) {
	const (
		snapshotLevel = "com/acme/app/1.0-SNAPSHOT/maven-metadata.xml"
		artifactLevel = "com/acme/app/maven-metadata.xml"
	)
	for _, tc := range []struct {
		name    string
		tune    func(*repo.Config)
		dropped bool
	}{
		{"unique", nil, true},
		{"non-unique", func(c *repo.Config) { c.SnapshotVersionBehavior = repo.NonUnique }, true},
		{"deployer", func(c *repo.Config) { c.SnapshotVersionBehavior = repo.Deployer }, false},
		{"another layout", func(c *repo.Config) { c.RepoLayoutRef = "simple-default" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, tc.tune)
			err := w.try(snapshotLevel, "<metadata/>")
			if s, _ := refusal(
				err,
			); tc.dropped && s != http.StatusAccepted ||
				!tc.dropped && err != nil {
				t.Fatalf("snapshot level: %v", err)
			}
			if w.stored(snapshotLevel) == tc.dropped {
				t.Fatalf("snapshot level stored: %v", w.stored(snapshotLevel))
			}
			if err := w.try(artifactLevel, "<metadata/>"); err != nil || !w.stored(artifactLevel) {
				t.Fatalf("artifact level: %v", err)
			}
		})
	}
	// An exploded archive leaves the dropped entry out and keeps the rest.
	w := newWorld(t, nil)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{snapshotLevel, "com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar"} {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(name))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, atomic := range []bool{false, true} {
		if _, err := w.items.Explode(w.ctx, item.Explode{
			Deploy: item.Deploy{
				Target: item.Target{Repo: w.c, Path: "bundle.zip"},
				Body:   bytes.NewReader(buf.Bytes()),
			},
			Atomic: atomic, CanOverwrite: true,
		}); err != nil {
			t.Fatalf("atomic %v: %v", atomic, err)
		}
		if w.stored(snapshotLevel) ||
			!w.stored("com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar") {
			t.Fatalf("atomic %v: the explode stored the wrong entries", atomic)
		}
	}
}

// Every upload named as a sidecar is a checksum of its file: one over 1024
// bytes is suspicious, one of any maven-metadata.xml is read and dropped
// with 200, and one whose file is missing answers 404, with Artifactory's
// messages.
func TestUnitChecksumUploads(t *testing.T) {
	w := newWorld(t, nil)
	if !w.items.OwnsSidecars(w.c) {
		t.Fatal("the Maven format does not own its sidecars")
	}
	jar := "com/acme/app/1.0/app-1.0.jar"
	w.deploy(jar)
	tgt := func(p string) item.Target { return item.Target{Repo: w.c, Path: p} }
	sha1 := w.item(jar).Actual.SHA1
	if _, err := w.items.RecordSidecar(w.ctx, tgt(jar), item.SHA1, sha1, 41); err != nil {
		t.Fatal(err)
	}
	if s, m := refusal(func() error {
		_, err := w.items.RecordSidecar(w.ctx, tgt(jar), item.SHA1, sha1, 1025)
		return err
	}()); s != http.StatusConflict ||
		m != "Suspicious checksum file, content length of 1025 bytes is bigger than allowed." {
		t.Fatalf("suspicious: %d %q", s, m)
	}
	for _, p := range []string{"com/acme/app/maven-metadata.xml", "com/acme/app/1.0-SNAPSHOT/maven-metadata.xml"} {
		_, err := w.items.RecordSidecar(w.ctx, tgt(p), item.SHA1, strings.Repeat("0", 40), 40)
		if s, _ := refusal(err); s != http.StatusOK {
			t.Fatalf("metadata %s: %v", p, err)
		}
	}
	_, err := w.items.RecordSidecar(w.ctx, tgt("com/acme/app/1.0/app-1.0.war"), item.MD5,
		strings.Repeat("0", 32), 32)
	var side *item.SidecarError
	if !errors.As(err, &side) || !errors.Is(err, store.ErrNotFound) || side.Message !=
		"Target file to set checksum on doesn't exist: maven-local:com/acme/app/1.0/app-1.0.war" {
		t.Fatalf("missing: %v", err)
	}
	_, err = w.items.RecordSidecar(w.ctx, tgt(jar), item.SHA1, strings.Repeat("A", 40), 40)
	if !errors.As(err, &side) || !errors.Is(err, item.ErrChecksumMismatch) || side.Message !=
		"Checksum error for '"+jar+".sha1': received '"+strings.Repeat("A", 40)+
			"' but actual is '"+sha1+"'" {
		t.Fatalf("mismatch: %v", err)
	}
	// The blacked-out check comes first.
	w.c.BlackedOut = on()
	if _, err := w.items.RecordSidecar(w.ctx, tgt(jar), item.SHA1, sha1, 2000); func() int {
		s, _ := refusal(err)
		return s
	}() != http.StatusNotFound {
		t.Fatalf("blacked out: %v", err)
	}
}

// A copy or move of a POM into a repository that checks POMs is refused
// when the POM does not match its new path, with 400 and a message that
// names the target alone.
func TestUnitTransferPOM(t *testing.T) {
	w := newWorld(t, nil)
	p := "com/acme/app/1.0/app-1.0.pom"
	w.deploy(p)
	for i, tc := range []struct {
		name, to string
		tune     func(*repo.Config)
		refused  bool
	}{
		{"same path", p, nil, false},
		{"another group", "com/other/app/1.0/app-1.0.pom", nil, true},
		{"suppressed", "com/other/app/1.0/app-1.0.pom", func(c *repo.Config) {
			c.SuppressPomConsistencyChecks = on()
		}, false},
		{"no longer a pom", "com/other/app/1.0/app-1.0.xml", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := w.repo("dst-"+string(rune('a'+i)), tc.tune)
			res, err := w.items.Transfer(w.ctx, item.Transfer{
				From: item.Target{Repo: w.c, Path: p},
				To:   item.Target{Repo: dst, Path: tc.to},
			})
			if !tc.refused {
				if err != nil || res.Failed != 0 {
					t.Fatalf("%+v %v", res, err)
				}
				return
			}
			if err != nil || res.Failed != 1 || !strings.HasSuffix(res.Messages[0].Text,
				"Failed to validate target path of pom: "+tc.to) {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
}

// latin1POM is a POM of com.acme:app:1.0 in the encoding its declaration
// names, with a name whose é is one ISO-8859-1 byte, invalid as UTF-8.
func latin1POM(encoding, group string) string {
	return `<?xml version="1.0" encoding="` + encoding + `"?>
<project><groupId>` + group + `</groupId><artifactId>app</artifactId><version>1.0</version>` +
		"<packaging>maven-plugin</packaging><name>Caf\xe9</name></project>"
}

// A POM is read in the encoding its XML declaration names, as Artifactory
// 7.x reads an ISO-8859-1 one on deploy and on copy (RTDEV-56287): the
// checks apply to what it says, and an encoding nothing knows makes it no
// POM.
func TestUnitPOMEncodings(t *testing.T) {
	p := "com/acme/app/1.0/app-1.0.pom"
	for _, enc := range []string{"ISO-8859-1", "iso-8859-1", "latin1", "windows-1252"} {
		t.Run(enc, func(t *testing.T) {
			w := newWorld(t, nil)
			if err := w.try(p, latin1POM(enc, "com.acme")); err != nil {
				t.Fatalf("deploy: %v", err)
			}
			props, err := w.db.Items().Properties(w.ctx, w.item(p).ID)
			if err != nil || props[maven.PluginProperty][0] != "true" {
				t.Fatalf("plugin property: %v %v", props, err)
			}
			pkg, err := w.db.Packages().Find(w.ctx, w.c.Key, "com.acme:app")
			if err != nil {
				t.Fatal(err)
			}
			pv, err := w.db.Packages().Version(w.ctx, pkg.ID, "1.0")
			if err != nil || !strings.Contains(string(pv.Metadata), `"name":"Café"`) {
				t.Fatalf("index metadata %s, %v", pv.Metadata, err)
			}
			// The checks still read the coordinates.
			status, msg := refusal(w.try(p, latin1POM(enc, "com.other")))
			if status != http.StatusConflict || !strings.Contains(msg, "'com/other/app/1.0'") {
				t.Fatalf("another group: %d %q", status, msg)
			}
			for i, move := range []bool{false, true} {
				dst := w.repo("dst-"+string(rune('a'+i)), nil)
				res, err := w.items.Transfer(w.ctx, item.Transfer{
					From: item.Target{Repo: w.c, Path: p}, To: item.Target{Repo: dst, Path: p},
					Move: move,
				})
				if err != nil || res.Failed != 0 {
					t.Fatalf("move %v: %+v %v", move, res, err)
				}
			}
		})
	}
	w := newWorld(t, nil)
	// "utf8" is no IANA name, but Java and browsers read it as UTF-8.
	utf8 := strings.Replace(latin1POM("utf8", "com.acme"), "\xe9", "é", 1)
	if err := w.try(p, utf8); err != nil {
		t.Fatalf("utf8: %v", err)
	}
	status, msg := refusal(w.try(p, latin1POM("x-no-such-charset", "com.acme")))
	if status != http.StatusConflict ||
		!strings.Contains(msg, `unsupported encoding "x-no-such-charset"`) {
		t.Fatalf("unknown encoding: %d %q", status, msg)
	}
}

// repo creates another Maven repository in w's store.
func (w *world) repo(key string, tune func(*repo.Config)) repo.Config {
	w.t.Helper()
	c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.Maven}
	repo.Defaults(&c)
	if tune != nil {
		tune(&c)
	}
	if err := w.db.Repos().Create(w.ctx, c); err != nil {
		w.t.Fatal(err)
	}
	return c
}

// A version's index metadata is what its POM says, the highest build's for
// a snapshot, and a reindex reads the same.
func TestUnitVersionMetadata(t *testing.T) {
	w := newWorld(t, nil)
	meta := func(pkg, v string) map[string]string {
		t.Helper()
		p, err := w.db.Packages().Find(w.ctx, w.c.Key, pkg)
		if err != nil {
			t.Fatal(err)
		}
		pv, err := w.db.Packages().Version(w.ctx, p.ID, v)
		if err != nil {
			t.Fatal(err)
		}
		if pv.Metadata == nil {
			return nil
		}
		var out map[string]string
		if err := json.Unmarshal(pv.Metadata, &out); err != nil {
			t.Fatal(err)
		}
		delete(out, "pom")
		return out
	}
	dir := "com/acme/app/"
	named := func(n string) string { return "<packaging>maven-plugin</packaging><name>" + n + "</name>" }
	w.deploy(dir + "1.0/app-1.0.jar")
	if got := meta("com.acme:app", "1.0"); got != nil {
		t.Fatalf("a version without a POM: %v", got)
	}
	if err := w.try(
		dir+"1.0/app-1.0.pom",
		pomXML("com.acme", "app", "1.0", named("App")),
	); err != nil {
		t.Fatal(err)
	}
	w.deploy(dir + "1.0/app-1.0-sources.jar")
	want := map[string]string{"packaging": "maven-plugin", "name": "App"}
	if got := meta("com.acme:app", "1.0"); !equal(got, want) {
		t.Fatalf("1.0: %v", got)
	}
	snap := dir + "1.0-SNAPSHOT/app-1.0-20261017.0900"
	for _, b := range []string{"00-1", "00-2"} {
		if err := w.try(
			snap+b+".pom",
			pomXML("com.acme", "app", "1.0-SNAPSHOT", named("Build "+b)),
		); err != nil {
			t.Fatal(err)
		}
	}
	if got := meta("com.acme:app", "1.0-SNAPSHOT"); got["name"] != "Build 00-2" {
		t.Fatalf("snapshot: %v", got)
	}
	w.delete(snap + "00-2.pom")
	if got := meta("com.acme:app", "1.0-SNAPSHOT"); got["name"] != "Build 00-1" {
		t.Fatalf("after the newest build went: %v", got)
	}
	// A reindex from nothing reads the POMs again, to the same metadata.
	h := maven.Handler{}
	env := format.Env{Store: w.db, Blobs: w.items.Blobs, Layouts: layouts{}}
	p, _ := w.db.Packages().Find(w.ctx, w.c.Key, "com.acme:app")
	if err := w.db.Packages().Delete(w.ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(w.ctx, env, w.c); err != nil {
		t.Fatal(err)
	}
	if got := meta("com.acme:app", "1.0"); !equal(got, want) {
		t.Fatalf("reindexed: %v", got)
	}
	// A POM the checks let through unread leaves its version only the
	// POM's checksum.
	w = newWorld(t, func(c *repo.Config) { c.SuppressPomConsistencyChecks = on() })
	if err := w.try(dir+"2.0/app-2.0.pom", "not a pom"); err != nil {
		t.Fatal(err)
	}
	if got := meta("com.acme:app", "2.0"); len(got) != 0 {
		t.Fatalf("unreadable POM: %v", got)
	}
	// Without a blob store, a POM cannot be read.
	noBlobs := format.Env{Store: w.db, Layouts: layouts{}}
	p, _ = w.db.Packages().Find(w.ctx, w.c.Key, "com.acme:app")
	_ = w.db.Packages().Delete(w.ctx, p.ID)
	if err := h.Reindex(w.ctx, noBlobs, w.c); err == nil {
		t.Fatal("a reindex read POMs without a blob store")
	}
}

func equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (w *world) item(p string) types.Item {
	w.t.Helper()
	it, err := w.db.Items().Get(context.Background(), w.c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

var errBlob = errors.New("blob boom")

// failingBlobs fails every read.
type failingBlobs struct{ format.Blobs }

func (failingBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, errBlob
}

// Admit and Inspect pass a store's failures back; a write of no kind they
// know, and a repository without a layout, change nothing.
func TestUnitAdmitFailures(t *testing.T) {
	ctx := context.Background()
	h := maven.Handler{}
	h.OwnsSidecars()
	c := repo.Config{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven}
	w := format.Write{Op: format.OpDeploy, Path: "com/acme/app/1.0/app-1.0.pom"}
	if _, err := h.Admit(
		ctx,
		format.Env{Layouts: layouts{fail: true}},
		c,
		w,
	); !errors.Is(
		err,
		errLayout,
	) {
		t.Fatalf("layout: %v", err)
	}
	w.Checksums.SHA256 = strings.Repeat("a", 64)
	if _, err := h.Inspect(ctx, format.Env{Blobs: failingBlobs{}}, c, w); !errors.Is(err, errBlob) {
		t.Fatalf("blob: %v", err)
	}
	if got, err := h.Inspect(ctx, format.Env{}, c, format.Write{
		Op: format.OpDeploy, Path: "com/acme/app/1.0/app-1.0.jar",
	}); got != nil || err != nil {
		t.Fatalf("a jar was inspected: %v %v", got, err)
	}
	if got, err := h.Admit(
		ctx,
		format.Env{Layouts: layouts{}},
		c,
		format.Write{Op: 99, Path: w.Path},
	); err != nil ||
		got.Path != w.Path {
		t.Fatalf("unknown op: %+v %v", got, err)
	}
	none := c
	none.RepoLayoutRef = "no-such-layout"
	if _, err := h.Admit(ctx, format.Env{Layouts: layouts{}}, none, format.Write{
		Op: format.OpDeploy, Path: "com/acme/app/1.0-SNAPSHOT/maven-metadata.xml",
	}); err != nil {
		t.Fatalf("no layout: %v", err)
	}
}
