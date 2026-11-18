package maven_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	cleanupPath = "com/acme/lib/1.0/lib-1.0.pom"
	cleanupPOM  = `<project><repositories><repository><id>top</id></repository></repositories><pluginRepositories/><profiles><profile><activation><activeByDefault>true</activeByDefault></activation><repositories><repository><id>active</id></repository></repositories></profile><profile><id>optional</id><repositories><repository><id>keep-optional</id></repository></repositories></profile></profiles></project>`
)

func pomCleanupWorld(t *testing.T, kind repo.PackageType, policy string, mutate ...func(*config.Config)) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t, mutate...)
	w.Repo.PackageType = kind
	yes := true
	w.Repo.SuppressPomConsistencyChecks = &yes
	w.Update()
	for _, c := range []repo.Config{
		{Key: "local", Class: repo.Local, PackageType: kind, SuppressPomConsistencyChecks: &yes},
		{Key: "virtual", Class: repo.Virtual, PackageType: kind, Repositories: []string{"local", "remote"}, PomRepositoryReferencesCleanupPolicy: repo.PomCleanupPolicy(policy)},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func deployCleanupPOM(t *testing.T, w *harness.RemoteWorld, body string) {
	t.Helper()
	sums := pomSums(body)
	res := w.Request("PUT", "/artifactory/local/"+cleanupPath, "admin", body, "X-Checksum-Sha1", sums.SHA1, "X-Checksum-Md5", sums.MD5, "X-Checksum-Sha256", sums.SHA256)
	if res.Code != 201 {
		t.Fatal(res.Code, res.Body.String())
	}
}

func pomSums(s string) kernel.Checksums {
	h := blob.NewHasher()
	_, _ = h.Write([]byte(s))
	d := h.Sum()
	return kernel.Checksums{SHA256: d.SHA256, SHA512: d.SHA512, SHA1: d.SHA1, MD5: d.MD5}
}

func TestAccMavenPOMCleanupPolicies(t *testing.T) {
	for _, tc := range []struct {
		kind              repo.PackageType
		policy            string
		changed, optional bool
	}{
		{repo.Maven, "", true, true},
		{repo.Maven, "discard_any_reference", true, false},
		{repo.Maven, "nothing", false, true},
		{repo.Gradle, "", true, true},
		{repo.Ivy, "", true, true},
		{repo.SBT, "", true, true},
		{repo.Generic, "", true, true},
		{repo.NPM, "", false, true},
	} {
		t.Run(string(tc.kind)+"-"+tc.policy, func(t *testing.T) {
			w := pomCleanupWorld(t, tc.kind, tc.policy)
			deployCleanupPOM(t, w, cleanupPOM)
			res := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "")
			body := res.Body.String()
			if res.Code != 200 {
				t.Fatal(res.Code, body)
			}
			if tc.changed {
				if strings.Contains(body, "<id>top</id>") || strings.Contains(body, "<id>active</id>") || strings.Contains(body, "pluginRepositories") {
					t.Fatal(body)
				}
				if strings.Contains(body, "keep-optional") != tc.optional {
					t.Fatal(body)
				}
			} else if body != cleanupPOM {
				t.Fatalf("untouched %q", body)
			}
			sums := pomSums(body)
			for name, want := range map[string]string{"X-Checksum-Sha1": sums.SHA1, "X-Checksum-Md5": sums.MD5, "X-Checksum-Sha256": sums.SHA256, "ETag": sums.SHA1} {
				if got := res.Header().Get(name); got != want {
					t.Fatalf("%s %q want %q", name, got, want)
				}
			}
			again := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "")
			if again.Body.String() != body || again.Header().Get("Last-Modified") != res.Header().Get("Last-Modified") {
				t.Fatal("unstable local copy")
			}
			if tc.changed {
				c, err := w.Runtime.Store.VirtualCopies().Get(t.Context(), "virtual", cleanupPath)
				if err != nil || c.Item.Actual != sums {
					t.Fatalf("stored copy %+v %v", c, err)
				}
				if c.Item.Modified.Before(time.Now().Add(-time.Minute)) {
					t.Fatal("old transform time")
				}
			}
			for alg, want := range map[string]string{"sha1": sums.SHA1, "md5": sums.MD5, "sha256": sums.SHA256} {
				side := w.Request("GET", "/artifactory/virtual/"+cleanupPath+"."+alg, "admin", "")
				if side.Code != 200 || strings.TrimSpace(side.Body.String()) != want {
					t.Fatalf("%s %d %q want %q", alg, side.Code, side.Body.String(), want)
				}
			}
			original := w.Request("GET", "/artifactory/local/"+cleanupPath, "admin", "")
			if original.Body.String() != cleanupPOM {
				t.Fatal("member changed")
			}
		})
	}
}

func TestAccMavenPOMSidecarFirstHEADAndRefresh(t *testing.T) {
	w := pomCleanupWorld(t, repo.Maven, "")
	deployCleanupPOM(t, w, cleanupPOM)
	original, err := w.Runtime.Store.Items().Get(t.Context(), "local", false, cleanupPath)
	if err != nil {
		t.Fatal(err)
	}
	original.Modified = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err = w.Runtime.Store.Items().Put(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	side := w.Request("GET", "/artifactory/virtual/"+cleanupPath+".sha1", "admin", "")
	res := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "")
	if res.Header().Get("Last-Modified") == original.Modified.Format(http.TimeFormat) {
		t.Fatal("member modification time leaked")
	}
	if side.Code != 200 || strings.TrimSpace(side.Body.String()) != res.Header().Get("X-Checksum-Sha1") {
		t.Fatalf("sidecar first %d %s", side.Code, side.Body.String())
	}
	head := w.Request("HEAD", "/artifactory/virtual/"+cleanupPath, "admin", "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != res.Header().Get("ETag") ||
		head.Header().Get("Content-Length") != res.Header().Get("Content-Length") {
		t.Fatal("HEAD", head.Code, head.Header())
	}
	conditional := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "", "If-None-Match", res.Header().Get("ETag"))
	if conditional.Code != 304 {
		t.Fatal("conditional", conditional.Code, conditional.Body.String())
	}
	changed := strings.Replace(cleanupPOM, "<id>optional</id>", "<id>changed-optional</id>", 1)
	deployCleanupPOM(t, w, changed)
	newer := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "")
	if newer.Code != 200 || !strings.Contains(newer.Body.String(), "changed-optional") || newer.Header().Get("ETag") == res.Header().Get("ETag") {
		t.Fatal("not refreshed", newer.Code, newer.Body.String())
	}
	// The virtual storage API still reports the member artifact, without
	// exposing its separate transformed representation as another item.
	storage := w.Request("GET", "/artifactory/api/storage/virtual/"+cleanupPath, "admin", "")
	if storage.Code != 200 || !strings.Contains(storage.Body.String(), pomSums(changed).SHA1) {
		t.Fatal("storage", storage.Code, storage.Body.String())
	}
}

func TestAccMavenPOMRemoteAndUnchangedFiles(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "remote"}[remote], func(t *testing.T) {
			w := pomCleanupWorld(t, repo.Maven, "")
			if remote {
				w.Upstream.Set("/"+cleanupPath, fakeupstream.Resource{Body: []byte(cleanupPOM)})
			} else {
				deployCleanupPOM(t, w, cleanupPOM)
			}
			first := w.Request("GET", "/artifactory/virtual/"+cleanupPath+".sha256", "admin", "")
			res := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "")
			if res.Code != 200 || strings.Contains(res.Body.String(), "<id>top</id>") || strings.TrimSpace(first.Body.String()) != pomSums(res.Body.String()).SHA256 {
				t.Fatal(first.Code, first.Body.String(), res.Code, res.Body.String())
			}
			for i, body := range []string{`<project><name>unchanged &amp; exact</name></project>`, `<project><repositories></project>`} {
				path := strings.TrimSuffix(cleanupPath, ".pom") + fmt.Sprintf("-%d.pom", i)
				if remote {
					w.Upstream.Set("/"+path, fakeupstream.Resource{Body: []byte(body)})
				} else {
					if r := w.Request("PUT", "/artifactory/local/"+path, "admin", body); r.Code != 201 {
						t.Fatal(r.Code, r.Body.String())
					}
				}
				res = w.Request("GET", "/artifactory/virtual/"+path, "admin", "")
				if res.Code != 200 || res.Body.String() != body {
					t.Fatalf("unchanged %d %q want %q", res.Code, res.Body.String(), body)
				}
			}
			jar := strings.TrimSuffix(cleanupPath, ".pom") + ".jar"
			if remote {
				w.Upstream.Set("/"+jar, fakeupstream.Resource{Body: []byte("unchanged jar")})
			} else {
				if r := w.Request("PUT", "/artifactory/local/"+jar, "admin", "unchanged jar"); r.Code != 201 {
					t.Fatal(r.Code)
				}
			}
			res = w.Request("GET", "/artifactory/virtual/"+jar, "admin", "")
			if res.Code != 200 || res.Body.String() != "unchanged jar" {
				t.Fatal("JAR", res.Code, res.Body.String())
			}
		})
	}
}

func TestAccMavenPOMRemoteCopyAndColdHEAD(t *testing.T) {
	w := pomCleanupWorld(t, repo.Maven, "")
	no := false
	w.Repo.StoreArtifactsLocally = &no
	w.Update()
	w.Upstream.Set("/"+cleanupPath, fakeupstream.Resource{Body: []byte(cleanupPOM)})
	head := w.Request("HEAD", "/artifactory/virtual/"+cleanupPath, "admin", "")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatal(head.Code, head.Body.String())
	}
	first, err := w.Runtime.Store.VirtualCopies().Get(t.Context(), "virtual", cleanupPath)
	if err != nil {
		t.Fatal(err)
	}
	res := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "")
	second, err := w.Runtime.Store.VirtualCopies().Get(t.Context(), "virtual", cleanupPath)
	if err != nil || res.Code != 200 || !second.Item.Modified.After(first.Item.Modified) {
		t.Fatal("remote copy not renewed", first, second, res.Code, err)
	}
	if res.Header().Get("X-Checksum-Sha1") != pomSums(res.Body.String()).SHA1 {
		t.Fatal("no-store checksums")
	}
	ranged := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "", "Range", "bytes=0-4")
	if ranged.Code != 206 || ranged.Body.String() != "<?xml" {
		t.Fatal("transformed range", ranged.Code, ranged.Body.String())
	}
}

type pomFaultBlobs struct {
	blob.Store
	write bool
}

func (b pomFaultBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if !b.write {
		return nil, blob.Info{}, errors.New("controlled POM read failure")
	}
	return b.Store.Open(ctx, sha)
}

func (b pomFaultBlobs) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	if b.write {
		return nil, errors.New("controlled POM write failure")
	}
	return b.Store.BeginUpload(ctx, opts...)
}

func TestAccMavenPOMTransformFailuresAndReadGate(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			w := pomCleanupWorld(t, repo.Maven, "")
			deployCleanupPOM(t, w, cleanupPOM)
			w.H = harness.Router(
				harness.Config{
					Store:  w.Runtime.Store,
					Blobs:  pomFaultBlobs{Store: w.Runtime.Blobs, write: write},
					Auth:   w.Runtime.Auth,
					Tokens: w.Runtime.Tokens,
					Keys:   w.Runtime.Keys,
				},
			)
			res := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "admin", "")
			prefix := "Failed to transform pom file"
			if write {
				prefix = "Failed to import file to local storage"
			}
			if res.Code != 404 || !strings.Contains(res.Body.String(), prefix) {
				t.Fatal(res.Code, res.Body.String())
			}
			denied := w.Request("GET", "/artifactory/virtual/"+cleanupPath, "denied", "")
			if denied.Code == 200 || strings.Contains(denied.Body.String(), "controlled POM") {
				t.Fatal("read gate", denied.Code, denied.Body.String())
			}
		})
	}
}

func TestAccMavenPOMNamedFolderIsNotTransformed(t *testing.T) {
	w := pomCleanupWorld(t, repo.Maven, "")
	created := w.Request("PUT", "/artifactory/local/folder.pom/", "admin", "")
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	res := w.Request("GET", "/artifactory/virtual/folder.pom.sha1", "admin", "")
	if res.Code != 404 || strings.Contains(res.Body.String(), "transform pom") {
		t.Fatal(res.Code, res.Body.String())
	}
}
