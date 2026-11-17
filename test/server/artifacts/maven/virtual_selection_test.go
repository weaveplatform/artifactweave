package maven_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func selectionWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := newMavenRemoteWorld(t)
	for _, c := range []repo.Config{
		{Key: "first", Class: repo.Local, PackageType: repo.Maven, SnapshotVersionBehavior: repo.Deployer},
		{Key: "second", Class: repo.Local, PackageType: repo.Maven, SnapshotVersionBehavior: repo.Deployer},
		{Key: "virtual", Class: repo.Virtual, PackageType: repo.Maven, Repositories: []string{"first", "second", "remote"}},
	} {
		if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func selectionPut(t *testing.T, w *remoteWorld, key, path, body string, modified int64) {
	t.Helper()
	remoteStatus(t, w.request("PUT", "/artifactory/"+key+"/"+path, "admin", body), 201)
	if strings.HasSuffix(path, ".sha1") {
		return
	}
	it, err := w.rt.Store.Items().Get(t.Context(), key, false, path)
	if err != nil {
		t.Fatal(err)
	}
	it.Modified = time.Unix(modified, 0).UTC()
	if _, err = w.rt.Store.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
}

func selectionConfig(t *testing.T, w *remoteWorld, key string, change func(*repo.Config)) {
	t.Helper()
	c, err := w.rt.Store.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	if err = w.rt.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func TestAccMavenVirtualCandidates(t *testing.T) {
	w := selectionWorld(t)
	const base = "org/acme/app/"
	const snap = base + "1.0-SNAPSHOT/"
	selectionPut(t, w, "first", base+"1.9/app-1.9.jar", "release1", 100)
	selectionPut(t, w, "second", base+"1.10/app-1.10.jar", "release2", 200)
	selectionPut(t, w, "first", base+"1.9/app-1.9.jar.sha1", sha1Hex("release1"), 100)
	selectionPut(t, w, "second", base+"1.10/app-1.10.jar.sha1", sha1Hex("release2"), 200)
	selectionPut(t, w, "first", snap+"app-1.0-20261117.100000-1.jar", "snapshot1", 300)
	selectionPut(t, w, "second", snap+"app-1.0-20261117.090000-1.jar", "snapshot2", 400)
	selectionPut(t, w, "second", base+"1.9/app-1.9.jar", "same-path-second", 500)
	for _, tc := range []struct{ path, body, filename string }{
		{base + "[RELEASE]/app-[RELEASE].jar", "release2", "app-1.10.jar"},
		{base + "[RELEASE]/app-[RELEASE].jar.sha1", sha1Hex("release2"), ""},
		{snap + "app-1.0-SNAPSHOT.jar", "snapshot2", "app-1.0-20261117.090000-1.jar"},
		{snap + "app-1.0-20261117.090000-1.jar", "snapshot2", "app-1.0-20261117.090000-1.jar"},
		{base + "1.9/app-1.9.jar", "release1", "app-1.9.jar"},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			r := w.request(method, "/artifactory/virtual/"+tc.path, "admin", "")
			remoteStatus(t, r, 200)
			if method == "GET" && r.Body.String() != tc.body {
				t.Fatal(tc.path, r.Body.String())
			}
			if tc.filename != "" && r.Header().Get("X-Artifactory-Filename") != tc.filename {
				t.Fatal(r.Header())
			}
			if method == "HEAD" && r.Body.Len() != 0 {
				t.Fatal("HEAD body")
			}
		}
	}
	trace := w.request("GET", "/artifactory/virtual/"+snap+"app-1.0-SNAPSHOT.jar?trace", "admin", "")
	for _, want := range []string{"Selecting current found resource as best candidate", "Returning found resource from second:", "Request succeeded"} {
		if !strings.Contains(trace.Body.String(), want) {
			t.Fatal(trace.Body.String())
		}
	}
	for _, tc := range []struct{ path, reason string }{
		{"org/acme/none/1.0-SNAPSHOT/none-1.0-SNAPSHOT.jar", "Artifact not found: org/acme/none/1.0-SNAPSHOT/none-1.0-SNAPSHOT.jar"},
		{"org/acme/none/[RELEASE]/none-[RELEASE].jar", "Could not find resource"},
	} {
		r := w.request("GET", "/artifactory/virtual/"+tc.path, "admin", "")
		status, msg := support.ArtifactoryError(t, r)
		if status != 404 || msg != tc.reason+"; Path: 'virtual:"+tc.path+"'" {
			t.Fatal(status, msg)
		}
	}
	// Priority candidates stop before the first non-priority member.
	selectionConfig(t, w, "first", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	r := w.request("GET", "/artifactory/virtual/"+base+"[RELEASE]/app-[RELEASE].jar", "admin", "")
	if r.Code != 200 || r.Body.String() != "release1" {
		t.Fatal(r.Code, r.Body.String())
	}
	selectionConfig(t, w, "first", func(c *repo.Config) { c.PriorityResolution = repo.Flex(false); c.HandleSnapshots = new(false) })
	r = w.request("GET", "/artifactory/virtual/"+snap+"app-1.0-SNAPSHOT.jar", "admin", "")
	if r.Code != 200 || r.Body.String() != "snapshot2" {
		t.Fatal(r.Code, r.Body.String())
	}
	selectionConfig(t, w, "second", func(c *repo.Config) { c.HandleReleases = new(false) })
	r = w.request("GET", "/artifactory/virtual/"+base+"[RELEASE]/app-[RELEASE].jar", "admin", "")
	if r.Code != 200 || r.Body.String() != "release1" {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestAccMavenVirtualRemoteSnapshots(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			w := selectionWorld(t)
			w.c.StoreArtifactsLocally = &store
			w.c.AssumedOfflinePeriodSecs = new(0)
			w.update()
			const path = "org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar"
			selectionPut(t, w, "first", path, "local", 100)
			body := sourceJAR(t, "new remote")
			mavenSource(w, path, body, "application/java-archive")
			w.up.Set(
				"/"+path,
				fakeupstream.Resource{
					Body:   body,
					Header: http.Header{"Content-Type": {"application/java-archive"}, "Last-Modified": {time.Unix(500, 0).UTC().Format(http.TimeFormat)}},
				},
			)
			for range 2 {
				r := w.request("GET", "/artifactory/virtual/"+path, "admin", "")
				if r.Code != 200 || r.Body.String() != string(body) {
					t.Fatal(r.Code, r.Body.String())
				}
			}
			trace := w.request("GET", "/artifactory/virtual/"+path+"?trace", "admin", "")
			if !strings.Contains(trace.Body.String(), "Returning found resource from remote:") ||
				strings.Contains(trace.Body.String(), "Searching for the resource within remote-cache") {
				t.Fatal(trace.Body.String())
			}
		})
	}
}

func TestAccMavenVirtualIntegrationAndMixedLayouts(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Maven, repo.Gradle, repo.Ivy} {
		t.Run(string(kind), func(t *testing.T) {
			w := selectionWorld(t)
			for _, key := range []string{"first", "second", "virtual"} {
				selectionConfig(t, w, key, func(c *repo.Config) {
					c.RepoLayoutRef = "gradle-default"
					if key == "virtual" {
						c.PackageType = kind
						c.RepoLayoutRef = repo.LayoutMaven1
						c.Repositories = []string{"first", "second"}
					}
				})
			}
			first := "org.acme/app/1.0-20261117120000/app-1.0-20261117120000.jar"
			second := "org.acme/app/1.0-20261117110000/app-1.0-20261117110000.jar"
			selectionPut(t, w, "first", first, "earlier modified", 100)
			selectionPut(t, w, "second", second, "later modified", 200)
			path := "org.acme/app/1.0-[INTEGRATION]/app-1.0-[INTEGRATION].jar"
			r := w.request("GET", "/artifactory/virtual/"+path, "admin", "")
			if r.Code != 200 || r.Body.String() != "later modified" {
				t.Fatal(r.Code, r.Body.String())
			}
			// A valid non-integration module deliberately keeps first-hit behavior,
			// even with an INTEGRATION token (7.161.26's exact mode condition).
			selectionConfig(t, w, "virtual", func(c *repo.Config) { c.RepoLayoutRef = "gradle-default" })
			r = w.request("GET", "/artifactory/virtual/"+path, "admin", "")
			if r.Code != 200 || r.Body.String() != "earlier modified" {
				t.Fatal(r.Code, r.Body.String())
			}
			// Maven members can retain Maven layout while an Ivy/Gradle virtual uses
			// its own layout's no-match snapshot fallback; translation stays MVN-06.
			for _, key := range []string{"first", "second"} {
				selectionConfig(t, w, key, func(c *repo.Config) { c.RepoLayoutRef = repo.LayoutMaven2 })
			}
			first = "org/acme/app/1.0-SNAPSHOT/app-1.0-20261117.120000-1.jar"
			second = "org/acme/app/1.0-SNAPSHOT/app-1.0-20261117.110000-1.jar"
			selectionPut(t, w, "first", first, "Maven first", 100)
			selectionPut(t, w, "second", second, "Maven second", 200)
			r = w.request("GET", "/artifactory/virtual/org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "admin", "")
			if r.Code != 200 || r.Body.String() != "Maven second" {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
}

func TestAccMavenVirtualSelectionRemembersFirstDenial(t *testing.T) {
	w := selectionWorld(t)
	selectionConfig(t, w, "virtual", func(c *repo.Config) { c.Repositories = []string{"first", "second"} })
	for _, path := range []string{"org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", "org/acme/app/[RELEASE]/app-[RELEASE].jar"} {
		r := w.request("GET", "/artifactory/virtual/"+path, "reader", "")
		status, message := support.ArtifactoryError(t, r)
		if status != 403 || !strings.Contains(message, "first:"+path) || !strings.HasSuffix(message, "; Path: 'virtual:"+path+"'") {
			t.Fatal(status, message)
		}
		selectionConfig(t, w, "virtual", func(c *repo.Config) { c.HideUnauthorizedResources = new(true) })
		r = w.request("GET", "/artifactory/virtual/"+path, "reader", "")
		status, message = support.ArtifactoryError(t, r)
		if status != 404 || message != "Resource not found; Path: 'virtual:"+path+"'" {
			t.Fatal(status, message)
		}
		selectionConfig(t, w, "virtual", func(c *repo.Config) { c.HideUnauthorizedResources = new(false) })
	}
}

func TestAccMavenVirtualRemoteReleaseAndOrigin(t *testing.T) {
	for _, store := range []bool{true, false} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			w := selectionWorld(t)
			w.c.StoreArtifactsLocally = &store
			w.update()
			selectionPut(t, w, "first", "org/acme/app/1.0/app-1.0.jar", "local release", 100)
			body := sourceJAR(t, "release 2.0")
			mavenSource(w, "org/acme/app/2.0/app-2.0.jar", body, "application/java-archive")
			mavenSource(
				w,
				"org/acme/app/maven-metadata.xml",
				[]byte(`<metadata><versioning><release>2.0</release><versions><version>2.0</version></versions></versioning></metadata>`),
				"application/xml",
			)
			const path = "org/acme/app/[RELEASE]/app-[RELEASE].jar"
			for range 2 {
				r := w.request("GET", "/artifactory/virtual/"+path, "admin", "")
				name := "app-2.0.jar"
				if !store {
					name = "app-%5BRELEASE%5D.jar"
				}
				if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), body) || r.Header().Get("X-Artifactory-Filename") != name {
					t.Fatal(r.Code, r.Header(), r.Body.String())
				}
			}
			const snap = "org/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar"
			unique := "org/acme/app/1.0-SNAPSHOT/app-1.0-20261117.120000-1.jar"
			selectionPut(t, w, "first", snap, "local snapshot", 100)
			mavenSource(w, unique, body, "application/java-archive")
			w.up.Set(
				"/"+unique,
				fakeupstream.Resource{
					Body:   body,
					Header: http.Header{"Content-Type": {"application/java-archive"}, "Last-Modified": {time.Unix(500, 0).UTC().Format(http.TimeFormat)}},
				},
			)
			mavenSource(
				w,
				"org/acme/app/1.0-SNAPSHOT/maven-metadata.xml",
				[]byte(
					`<metadata><versioning><snapshotVersions><snapshotVersion><extension>jar</extension><value>1.0-20261117.120000-1</value></snapshotVersion></snapshotVersions></versioning></metadata>`,
				),
				"application/xml",
			)
			r := w.request("GET", "/artifactory/virtual/"+snap, "admin", "")
			if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), body) {
				t.Fatal(r.Code, r.Body.String())
			}
			before := len(w.up.Requests())
			// R7 removes upstream steps; snapshot selection also excludes standalone
			// caches (RTFACT-7095), so only the local candidate remains.
			r = w.request("GET", "/artifactory/virtual/"+snap, "admin", "", "X-Artifactory-Originated", "another-node")
			want := "local snapshot"
			if r.Code != 200 || r.Body.String() != want || len(w.up.Requests()) != before {
				t.Fatal(r.Code, r.Body.String(), w.up.Requests())
			}
		})
	}
}

func TestAccMavenVirtualUniqueSnapshotStopsAtExactMember(t *testing.T) {
	w := selectionWorld(t)
	const path = "org/acme/app/1.0-SNAPSHOT/app-1.0-20261117.120000-1.jar"
	selectionPut(t, w, "first", path, "exact first", 100)
	selectionPut(t, w, "second", path, "newer second", 200)
	r := w.request("GET", "/artifactory/virtual/"+path, "admin", "")
	if r.Code != 200 || r.Body.String() != "exact first" {
		t.Fatal(r.Code, r.Body.String())
	}
	trace := w.request("GET", "/artifactory/virtual/"+path+"?trace", "admin", "")
	if !strings.Contains(trace.Body.String(), "Selecting a unique snapshot") || strings.Contains(trace.Body.String(), "Searching for the resource within second") {
		t.Fatal(trace.Body.String())
	}
}
