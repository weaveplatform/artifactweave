package maven_test

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// newMavenWorld is an item world with a Maven repository, maven-local, and
// the formats' work registered with a queue that no worker runs, so work is
// done when a read asks for it.
func newMavenWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := newItemWorld(t, false, func(cfg *server.Config) {
		q, err := work.New(cfg.Store, work.Options{})
		if err != nil {
			t.Fatal(err)
		}
		derived := &item.Service{Store: cfg.Store, Blobs: cfg.Blobs, Work: q}
		if err := derived.RegisterWork(q); err != nil {
			t.Fatal(err)
		}
		cfg.Work = q
	})
	c := repo.Config{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven}
	if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	return w
}

// calculateMetadata answers 200 with an empty plain-text body once the
// work is queued, and the next read of the metadata has it (#282).
func TestCalculateMetadataOverHTTP(t *testing.T) {
	w := newMavenWorld(t)
	base := "/artifactory/maven-local/com/acme/app/"
	w.put(base+"1.0/app-1.0.pom", mavenPOM("com.acme", "app", "1.0"))
	wantStatus(t, w.do("GET", base+"maven-metadata.xml", "admin", ""), http.StatusOK)
	wantStatus(t, w.do("DELETE", base+"maven-metadata.xml", "admin", ""), http.StatusNoContent)
	wantStatus(t, w.do("GET", base+"maven-metadata.xml", "admin", ""), http.StatusNotFound)

	rec := w.do(
		"POST",
		"/artifactory/api/maven/calculateMetadata/maven-local/com/acme",
		"admin",
		"",
	)
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.Len() != 0 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("%q %q", rec.Header().Get("Content-Type"), rec.Body)
	}
	rec = w.do("GET", base+"maven-metadata.xml", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "<release>1.0</release>") {
		t.Fatalf("%s", rec.Body)
	}
}

// generatePom writes the jar's own POM through the deploy rules, so a POM
// whose coordinates are not its path's is refused with their 409 (#282).
func TestGeneratePomOverHTTP(t *testing.T) {
	w := newMavenWorld(t)
	jar := func(pom string) string {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		f, err := zw.Create("META-INF/maven/com.acme/app/pom.xml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(pom)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	base := "/artifactory/maven-local/com/acme/app/"
	gen := "/artifactory/api/maven/generatePom/maven-local/com/acme/app/"
	w.put(base+"1.0/app-1.0.jar", jar(mavenPOM("com.acme", "app", "1.0")))
	rec := w.do("POST", gen+"1.0/app-1.0.jar", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.Len() != 0 {
		t.Fatalf("body %q", rec.Body)
	}
	rec = w.do("GET", base+"1.0/app-1.0.pom", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != mavenPOM("com.acme", "app", "1.0") {
		t.Fatalf("POM %s", rec.Body)
	}

	w.put(base+"2.0/app-2.0.jar", jar(mavenPOM("com.acme", "app", "3.0")))
	wantStatus(t, w.do("POST", gen+"2.0/app-2.0.jar", "admin", ""), http.StatusConflict)
	wantStatus(t, w.do("GET", base+"2.0/app-2.0.pom", "admin", ""), http.StatusNotFound)
}
