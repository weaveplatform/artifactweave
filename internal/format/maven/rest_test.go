package maven_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// jarOf is a jar holding each name with its content.
func jarOf(t *testing.T, entries ...string) string {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for i := 0; i+1 < len(entries); i += 2 {
		f, err := zw.Create(entries[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(entries[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// calculate runs calculateMetadata's work on folder and returns what it
// queued.
func (w *metaWorld) calculate(folder string, recursive bool) map[string]bool {
	w.t.Helper()
	if err := maven.CalculateMetadata(w.ctx, w.items.Env(), w.c, folder, recursive); err != nil {
		w.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, kind := range []string{maven.KindMetadata, maven.KindPlugins} {
		for _, key := range []string{
			"com.acme:app", "com.acme:lib", "com.acme:gone", "com.acme.app:1.0", "com.acme",
			"com.acme.app",
		} {
			if _, err := w.db.WorkQueue().Get(w.ctx, kind, w.c.Key+"/"+key); err == nil {
				out[kind+" "+key] = true
			}
		}
	}
	return out
}

// calculateMetadata rebuilds the index under the folder from the tree and
// queues the metadata of every package there, and of the package each
// maven-metadata.xml there describes; a read then sees it (#282).
func TestUnitCalculateMetadata(t *testing.T) {
	w := newMetaWorld(t, nil)
	w.deploy("com/acme/app/1.0/app-1.0.pom", nil)
	w.deploy("com/acme/app/1.0/app-1.0.jar", nil)
	w.deploy("com/acme/lib/2.0/lib-2.0.pom", nil)
	w.deploy("com/acme/gone/maven-metadata.xml", nil)
	// Settle what the deploys queued, then lose app's metadata and lib's
	// index entry, as a repair would find them.
	w.get("com/acme/app/maven-metadata.xml")
	w.get("com/acme/lib/maven-metadata.xml")
	w.get("com/acme/gone/maven-metadata.xml")
	w.delete("com/acme/app/maven-metadata.xml")
	p, err := w.db.Packages().Find(w.ctx, w.c.Key, "com.acme:lib")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Packages().Delete(w.ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if len(w.queued()) != 0 {
		t.Fatalf("work left: %v", w.queued())
	}

	got := w.calculate("com/acme", true)
	for _, want := range []string{
		"maven.metadata com.acme:app", "maven.metadata com.acme:lib",
		"maven.metadata com.acme:gone", "maven.plugins com.acme",
	} {
		if !got[want] {
			t.Errorf("%s not queued: %v", want, got)
		}
	}
	if body, _, ok := w.get("com/acme/app/maven-metadata.xml"); !ok ||
		!strings.Contains(body, "<release>1.0</release>") {
		t.Fatalf("app's metadata:\n%s", body)
	}
	if vs, _, err := maven.Versions(w.ctx, w.db, w.c, "com.acme:lib"); err != nil || len(vs) != 1 {
		t.Fatalf("lib's index: %v %v", vs, err)
	}
	for _, p := range []string{"com/acme/lib/maven-metadata.xml", "com/acme/gone/maven-metadata.xml"} {
		w.get(p)
	}

	// noneRecursive takes the folder's own files and metadata only.
	got = w.calculate("com/acme/app/1.0", false)
	if !got["maven.metadata com.acme:app"] || got["maven.metadata com.acme:lib"] {
		t.Fatalf("one folder: %v", got)
	}
	for _, kind := range []string{maven.KindMetadata, maven.KindPlugins} {
		if _, err := w.q.RunDue(w.ctx, kind); err != nil {
			t.Fatal(err)
		}
	}
	// The root has no metadata of its own, and nothing directly in it.
	if got := w.calculate("", false); len(got) != 0 {
		t.Fatalf("the root alone: %v", got)
	}
	if got := w.calculate("/", true); !got["maven.metadata com.acme:app"] {
		t.Fatalf("the whole repository: %v", got)
	}
}

// Every failure calculateMetadata meets comes back from it, and nothing is
// queued.
func TestUnitCalculateMetadataFailures(t *testing.T) {
	w := newMetaWorld(t, nil)
	w.deploy("com/acme/app/1.0/app-1.0.pom", nil)
	ok := func(context.Context, string, string, []byte) error { return nil }
	for name, env := range map[string]format.Env{
		"layout":  {Store: w.db, Layouts: layouts{fail: true}, Enqueue: ok},
		"search":  {Store: workFaults{w.db, "Search"}, Layouts: layouts{}, Enqueue: ok},
		"enqueue": {Store: w.db, Layouts: layouts{}, Blobs: w.blobs, Now: time.Now, Enqueue: refuse},
	} {
		if err := maven.CalculateMetadata(w.ctx, env, w.c, "com", true); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func refuse(context.Context, string, string, []byte) error { return errWork }

// generatePom's POM: the jar's own embedded POM, found among a shaded
// jar's, or a default POM, at the main artifact's descriptor path; a
// unique build's POM is that build's (#282).
func TestUnitGeneratePOM(t *testing.T) {
	w := newMetaWorld(t, nil)
	own := pomOf("com.acme", "app", "1.0", "jar", "embedded")
	w.deploy("com/acme/app/1.0/app-1.0.jar", nil, jarOf(t,
		"META-INF/maven/org.dep/dep/pom.xml", pomOf("org.dep", "dep", "9", "jar", ""),
		"META-INF/maven/com.acme/app/pom.xml", own,
	))
	w.deploy("com/acme/app/1.0/app-1.0-sources.jar", nil, "not a zip")
	w.deploy("com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.war", nil)
	w.deploy("com/acme/readme.txt", nil)
	for _, tc := range []struct {
		jar, at string
		want    []string
	}{
		{"com/acme/app/1.0/app-1.0.jar", "com/acme/app/1.0/app-1.0.pom", []string{"embedded"}},
		{"com/acme/app/1.0/app-1.0-sources.jar", "com/acme/app/1.0/app-1.0.pom", []string{
			"<groupId>com.acme</groupId>", "<artifactId>app</artifactId>",
			"<version>1.0</version>", "<packaging>jar</packaging>",
			"<description>Artifactory auto generated POM</description>",
			"<modelVersion>4.0.0</modelVersion>",
		}},
		{
			"com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.war",
			"com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1.pom",
			[]string{"<version>1.0-SNAPSHOT</version>", "<packaging>war</packaging>"},
		},
	} {
		at, body, err := maven.GeneratePOM(w.ctx, w.items.Env(), w.c, w.item(tc.jar))
		if err != nil || at != tc.at {
			t.Fatalf("%s: %q %v", tc.jar, at, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s: no %s in\n%s", tc.jar, want, body)
			}
		}
	}
	for _, p := range []string{"com/acme/readme.txt", "com/acme/app/1.0/app-1.0.pom"} {
		if p == "com/acme/app/1.0/app-1.0.pom" {
			w.deploy(p, nil)
		}
		_, _, err := maven.GeneratePOM(w.ctx, w.items.Env(), w.c, w.item(p))
		var refused *format.RejectionError
		if !errors.As(err, &refused) || refused.Status != http.StatusBadRequest {
			t.Errorf("%s: %v", p, err)
		}
	}
	jar := w.item("com/acme/app/1.0/app-1.0.jar")
	if _, _, err := maven.GeneratePOM(w.ctx, format.Env{
		Store: w.db, Layouts: layouts{}, Blobs: closedBlobs{w.blobs},
	}, w.c, jar); !errors.Is(err, errWork) {
		t.Errorf("unreadable jar: %v", err)
	}
	if _, _, err := maven.GeneratePOM(w.ctx, format.Env{
		Store: w.db, Layouts: layouts{fail: true},
	}, w.c, jar); err == nil {
		t.Error("unreadable layout: no error")
	}
}

// Under a layout whose descriptor is no POM, as gradle-default's Ivy file,
// the POM takes the artifact's own path with the extension pom.
func TestUnitGeneratePOMOtherLayout(t *testing.T) {
	w := newMetaWorld(t, func(c *repo.Config) {
		c.PackageType, c.RepoLayoutRef = repo.Gradle, "gradle-default"
	})
	w.deploy("org.acme/app/1.0/app-1.0.jar", nil)
	at, _, err := maven.GeneratePOM(
		w.ctx,
		w.items.Env(),
		w.c,
		w.item("org.acme/app/1.0/app-1.0.jar"),
	)
	if err != nil || at != "org.acme/app/1.0/app-1.0.pom" {
		t.Fatalf("%q %v", at, err)
	}
}

// item returns the item at p.
func (w *metaWorld) item(p string) types.Item {
	w.t.Helper()
	it, err := w.db.Items().Get(w.ctx, w.c.Key, false, p)
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}
