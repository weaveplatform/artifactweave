package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// A GET of maven-metadata.xml, or of its checksum, straight after a deploy
// returns the metadata that deploy changed: the read runs the queued work,
// since no worker runs here.
func TestMavenMetadataRebuiltOnRead(t *testing.T) {
	w := newItemWorld(t, false, func(cfg *Config) {
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
	base := "/artifactory/maven-local/com/acme/app/"
	wantStatus(t, w.do("PUT", base+"1.0/app-1.0.pom", "admin", "<project/>"), http.StatusCreated)
	rec := w.do("GET", base+"maven-metadata.xml", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "<latest>1.0</latest>") {
		t.Fatalf("%s", rec.Body)
	}

	wantStatus(t, w.do("PUT", base+"1.1/app-1.1.pom", "admin", "<project/>"), http.StatusCreated)
	rec = w.do("GET", base+"maven-metadata.xml.sha1", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	meta := w.do("HEAD", base+"maven-metadata.xml", "admin", "")
	if got := meta.Header().Get("X-Checksum-Sha1"); got == "" || got != rec.Body.String() {
		t.Errorf("checksum %q, header %q", rec.Body, got)
	}
	// The metadata's checksums are the server's under client-checksums too:
	// Maven's upload of one is dropped, and the computed one is served.
	wantStatus(t, w.do("PUT", base+"maven-metadata.xml.md5", "admin", strings.Repeat("0", 32)),
		http.StatusOK)
	rec = w.do("GET", base+"maven-metadata.xml.md5", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if got := meta.Header().Get("X-Checksum-Md5"); got == "" || got != rec.Body.String() {
		t.Errorf("md5 %q, header %q", rec.Body, got)
	}
	if body := w.do("GET", base+"maven-metadata.xml", "admin", "").Body.String(); !strings.Contains(
		body, "<version>1.1</version>",
	) {
		t.Errorf("after deploying 1.1:\n%s", body)
	}
}
