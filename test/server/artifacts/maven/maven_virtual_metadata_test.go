package maven_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const virtualMetadataPath = "com/acme/lib/maven-metadata.xml"

func virtualMetadata(version string) string {
	return `<metadata modelVersion="1.0.0"><groupId>com.acme</groupId><artifactId>lib</artifactId><versioning><versions><version>` + version + `</version></versions><lastUpdated>20261117120000</lastUpdated></versioning></metadata>`
}

func metadataWorld(t *testing.T, kind repo.PackageType) *harness.RemoteWorld {
	t.Helper()
	w := pomCleanupWorld(t, kind, "")
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: "second", Class: repo.Local, PackageType: kind})); err != nil {
		t.Fatal(err)
	}
	c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"local", "second", "remote"}
	if err := w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	return w
}

func putMetadata(t *testing.T, w *harness.RemoteWorld, key, p, body string, modified int) {
	t.Helper()
	res := w.Request("PUT", "/artifactory/"+key+"/"+p, "admin", body)
	if res.Code != 201 {
		t.Fatal(res.Code, res.Body.String())
	}
	it, err := w.Runtime.Store.Items().Get(t.Context(), key, false, p)
	if err != nil {
		t.Fatal(err)
	}
	it.Modified = time.Unix(int64(modified), 0)
	if _, err = w.Runtime.Store.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
}

func TestAccMavenVirtualMetadataMergeAndSidecars(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Maven, repo.Generic} {
		t.Run(string(kind), func(t *testing.T) {
			w := metadataWorld(t, kind)
			putMetadata(t, w, "local", virtualMetadataPath, virtualMetadata("1.0"), 100)
			putMetadata(t, w, "second", virtualMetadataPath, virtualMetadata("2.0"), 200)
			w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Body: []byte(virtualMetadata("3.0"))})
			res := w.Request("GET", "/artifactory/virtual/"+virtualMetadataPath, "admin", "")
			body := res.Body.String()
			if res.Code != 200 || !strings.Contains(body, "<version>1.0</version>") || !strings.Contains(body, "<version>2.0</version>") ||
				!strings.Contains(body, "<version>3.0</version>") ||
				!strings.Contains(body, `modelVersion="1.0.0"`) {
				t.Fatal(res.Code, body)
			}
			sums := pomSums(body)
			for key, want := range map[string]string{"X-Checksum-Sha1": sums.SHA1, "ETag": sums.SHA1, "X-Checksum-Md5": sums.MD5, "X-Checksum-Sha256": sums.SHA256, "Content-Length": fmt.Sprint(len(body)), "Content-Type": "application/xml"} {
				if res.Header().Get(key) != want {
					t.Fatalf("%s=%q want %q", key, res.Header().Get(key), want)
				}
			}
			for _, method := range []string{"GET", "HEAD"} {
				for suffix, want := range map[string]string{"": body, ".sha1": sums.SHA1, ".md5": sums.MD5, ".sha256": sums.SHA256} {
					r := w.Request(method, "/artifactory/virtual/"+virtualMetadataPath+suffix, "admin", "")
					if r.Code != 200 || method == "GET" && strings.TrimSpace(r.Body.String()) != strings.TrimSpace(want) || method == "HEAD" && r.Body.Len() != 0 {
						t.Fatal(method, suffix, r.Code, r.Body.String())
					}
				}
			}
			original := w.Request("GET", "/artifactory/local/"+virtualMetadataPath, "admin", "")
			if original.Body.String() != virtualMetadata("1.0") {
				t.Fatal("direct local rewritten")
			}
			conditional := w.Request("GET", "/artifactory/virtual/"+virtualMetadataPath, "admin", "", "If-None-Match", sums.SHA1)
			if conditional.Code != 304 {
				t.Fatal(conditional.Code, conditional.Body.String())
			}
		})
	}
}

func TestAccMavenVirtualMetadataPriorityMalformedAndMiss(t *testing.T) {
	w := metadataWorld(t, repo.Generic)
	for _, key := range []string{"local", "second"} {
		c, err := w.Runtime.Store.Repos().Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		c.PriorityResolution = repo.Flex(true)
		if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	putMetadata(t, w, "local", virtualMetadataPath, `<metadata><malformed>`, 100)
	putMetadata(t, w, "second", virtualMetadataPath, virtualMetadata("2.0"), 200)
	w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Body: []byte(virtualMetadata("3.0"))})
	r := w.Request("GET", "/artifactory/virtual/"+virtualMetadataPath, "admin", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "2.0</version>") || strings.Contains(r.Body.String(), "3.0</version>") ||
		r.Header().Get("Last-Modified") != time.Unix(200, 0).UTC().Format(http.TimeFormat) {
		t.Fatal(r.Code, r.Body.String(), r.Header())
	}
	missing := w.Request("GET", "/artifactory/virtual/missing/maven-metadata.xml", "admin", "")
	if missing.Code != 404 || !strings.Contains(missing.Body.String(), "Maven metadata not found for 'missing/maven-metadata.xml'.; Path: 'virtual:missing/maven-metadata.xml'") {
		t.Fatal(missing.Code, missing.Body.String())
	}
	denied := w.Request("GET", "/artifactory/virtual/"+virtualMetadataPath, "denied", "")
	if denied.Code != 403 || !strings.Contains(denied.Body.String(), "local:com/acme/lib/maven-metadata.xml") {
		t.Fatal(denied.Code, denied.Body.String())
	}
	// A user allowed only on remote must pass over the local refusals.
	reader := w.Request("GET", "/artifactory/virtual/"+virtualMetadataPath, "reader", "")
	if reader.Code != 200 || !strings.Contains(reader.Body.String(), "3.0</version>") || strings.Contains(reader.Body.String(), "2.0</version>") {
		t.Fatal(reader.Code, reader.Body.String())
	}
	c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.HideUnauthorizedResources = new(true)
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	hidden := w.Request("GET", "/artifactory/virtual/"+virtualMetadataPath, "denied", "")
	if hidden.Code != 404 || strings.Contains(hidden.Body.String(), "forbidden") {
		t.Fatal(hidden.Code, hidden.Body.String())
	}
}

func TestAccMavenVirtualMetadataSnapshotsAndIndexerBypass(t *testing.T) {
	w := metadataWorld(t, repo.Generic)
	p := "com/acme/lib/1-SNAPSHOT/maven-metadata.xml"
	body := `<metadata><versioning><snapshotVersions><snapshotVersion><extension>jar</extension><value>1-20261117.120000-1</value></snapshotVersion></snapshotVersions></versioning></metadata>`
	putMetadata(t, w, "local", p, body, 100)
	putMetadata(t, w, "second", p, strings.ReplaceAll(body, "-1</value>", "-2</value>"), 200)
	c, err := w.Runtime.Store.Repos().Get(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	c.HandleSnapshots = new(false)
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for ua, want := range map[string]bool{"Apache-Maven/3.9.0": true, "Java/17": false, "Apache Ivy/2.0": false} {
		r := w.Request("GET", "/artifactory/virtual/"+p, "admin", "", "User-Agent", ua)
		if r.Code != 200 || strings.Contains(r.Body.String(), "snapshotVersions") != want || strings.Contains(r.Body.String(), "-2</value>") {
			t.Fatal(ua, r.Code, r.Body.String())
		}
	}
	w.H = harness.Router(harness.Config{
		Store: w.Runtime.Store, Blobs: w.Runtime.Blobs, Auth: w.Runtime.Auth, Tokens: w.Runtime.Tokens,
		Keys: w.Runtime.Keys, RemoteSources: w.Runtime.Artifacts.RemoteSources(),
		FormatSettings: ecosystem.Settings{Maven: maven.Settings{MetadataVersion3Enabled: new(false)}},
	})
	disabled := w.Request("GET", "/artifactory/virtual/"+p, "admin", "")
	if disabled.Code != 200 || strings.Contains(disabled.Body.String(), "snapshotVersions") {
		t.Fatal(disabled.Code, disabled.Body.String())
	}
	putMetadata(t, w, "local", ".index/maven-metadata.xml", "opaque index bytes", 100)
	r := w.Request("GET", "/artifactory/virtual/.index/maven-metadata.xml", "admin", "")
	if r.Code != 200 || r.Body.String() != "opaque index bytes" {
		t.Fatal(r.Code, r.Body.String())
	}
	calc := w.Request("POST", "/artifactory/api/maven/calculateMetadata/virtual/", "admin", "")
	if calc.Code != 404 || !strings.Contains(calc.Body.String(), "Unable to find local repository 'virtual'.") {
		t.Fatal(calc.Code, calc.Body.String())
	}
}
