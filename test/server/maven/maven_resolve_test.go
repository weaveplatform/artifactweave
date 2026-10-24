package maven_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// GET and HEAD of a version token or a -SNAPSHOT name serve the file the
// Maven format resolves, with its checksums, through a virtual repository
// too, and filtered by matrix parameters; the storage API reads the path as
// given.
func TestAccMavenDownloadsResolveVersions(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "mvn-local", Class: repo.Local, PackageType: repo.Maven},
		{Key: "mvn", Class: repo.Virtual, PackageType: repo.Maven, Repositories: []string{"mvn-local"}},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	dir := "/artifactory/mvn-local/com/acme/app/"
	for path, body := range map[string]string{
		"1.9/app-1.9.jar;tier=gold":                        "jar 1.9",
		"1.10/app-1.10.jar;tier=silver":                    "jar 1.10",
		"1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar":       "build 1",
		"1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar":       "build 2",
		"1.0-SNAPSHOT/app-1.0-20261017.090000-1-tests.jar": "tests 1",
	} {
		wantStatus(t, w.do("PUT", dir+path, "admin", body), http.StatusCreated)
	}
	// Maven sends the SHA-1 after the file; under client-checksums the
	// sidecar serves it, and nothing until it is sent.
	wantStatus(t, w.do("PUT", dir+"1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar.sha1", "admin",
		sha1Hex("build 2")), http.StatusCreated)
	for _, tc := range []struct {
		method, path, want string
	}{
		{"GET", dir + "[RELEASE]/app-[RELEASE].jar", "jar 1.10"},
		{"GET", dir + "[RELEASE]/app-[RELEASE].jar;tier=gold", "jar 1.9"},
		{"GET", "/artifactory/mvn/com/acme/app/[RELEASE]/app-[RELEASE].jar", "jar 1.10"},
		{"GET", dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "build 2"},
		{"GET", dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT-tests.jar", "tests 1"},
		{"GET", dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar.sha1", sha1Hex("build 2")},
		{"HEAD", dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", ""},
	} {
		rec := w.do(tc.method, tc.path, "reader", "")
		wantStatus(t, rec, http.StatusOK)
		if rec.Body.String() != tc.want {
			t.Errorf("%s %s: %q, want %q", tc.method, tc.path, rec.Body, tc.want)
		}
		if tc.method == "HEAD" && rec.Header().Get("X-Checksum-Sha1") != sha1Hex("build 2") {
			t.Errorf("HEAD checksum %q", rec.Header().Get("X-Checksum-Sha1"))
		}
	}
	wantStatus(t, w.do("GET", dir+"[RELEASE]/app-[RELEASE].war", "reader", ""), http.StatusNotFound)
	wantStatus(
		t,
		w.do(
			"GET",
			"/artifactory/api/storage/mvn-local/com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar",
			"reader",
			"",
		),
		http.StatusNotFound,
	)
}

// Under a layout without both integration revisions a version token is part
// of the name looked up, so the download is 404 though a release exists.
func TestAccMavenLayoutWithoutIntegrationRevisions(t *testing.T) {
	w := newItemWorld(t, false)
	c := repo.Config{
		Key:           "m1-local",
		Class:         repo.Local,
		PackageType:   repo.Maven,
		RepoLayoutRef: repo.LayoutMaven1,
	}
	if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/m1-local/acme/jars/"
	wantStatus(t, w.do("PUT", base+"app-2.0.jar", "admin", "jar 2.0"), http.StatusCreated)
	wantStatus(t, w.do("GET", base+"app-[RELEASE].jar", "reader", ""), http.StatusNotFound)
	wantStatus(t, w.do("GET", base+"app-2.0.jar", "reader", ""), http.StatusOK)
}

// An ISO-8859-1 POM is deployed and copied with the POM checks on.
func TestAccMavenLatin1POM(t *testing.T) {
	w := newItemWorld(t, false)
	for _, key := range []string{"latin-local", "latin-copy"} {
		c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.Maven}
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	pom := `<?xml version="1.0" encoding="ISO-8859-1"?>` +
		"<project><groupId>com.acme</groupId><artifactId>app</artifactId><version>2.0</version>" +
		"<name>Caf\xe9</name></project>"
	path := "com/acme/app/2.0/app-2.0.pom"
	wantStatus(t, w.do("PUT", "/artifactory/latin-local/"+path, "admin", pom), http.StatusCreated)
	wantStatus(t, w.do("POST", "/artifactory/api/copy/latin-local/"+path+"?to=/latin-copy/"+path,
		"admin", ""), http.StatusOK)
}
