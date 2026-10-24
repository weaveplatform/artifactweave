package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Maven checksum wire protocol
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func newMavenRemoteWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := newRemoteWorld(t)
	w.c = *repo.Defaults(&repo.Config{Key: w.c.Key, Class: repo.Remote, PackageType: repo.Maven, URL: w.up.URL})
	w.update()
	return w
}

func sourcePOM(artifact, version string) string {
	return "<project><modelVersion>4.0.0</modelVersion><groupId>org.acme</groupId><artifactId>" + artifact + "</artifactId><version>" + version + "</version></project>"
}

func sourceJAR(t *testing.T, content string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func mavenSource(w *remoteWorld, path string, data []byte, contentType string) {
	w.up.Set(
		"/"+path,
		fakeupstream.Resource{Body: data, Header: http.Header{"Content-Type": {contentType}}},
	)
	sum := sha1.Sum(data)
	w.up.Set(
		"/"+path+".sha1",
		fakeupstream.Resource{
			Body:   []byte(hex.EncodeToString(sum[:])),
			Header: http.Header{"Content-Type": {"text/plain"}},
		},
	)
}

func TestAccMavenRemoteMetadataArtifactsAndOutage(t *testing.T) {
	w := newMavenRemoteWorld(t)
	jar := sourceJAR(t, "release")
	mavenSource(w, "org/acme/app/1.0/app-1.0.jar", jar, "application/java-archive")
	mavenSource(
		w,
		"org/acme/app/1.0/app-1.0.pom",
		[]byte(sourcePOM("app", "1.0")),
		"application/xml",
	)
	metadata := `<metadata><groupId>org.acme</groupId><artifactId>app</artifactId><versioning><latest>2.0</latest><release>2.0</release><versions><version>1.0</version><version>2.0</version></versions></versioning></metadata>`
	mavenSource(w, "org/acme/app/maven-metadata.xml", []byte(metadata), "text/xml")
	for _, p := range []string{"org/acme/app/1.0/app-1.0.pom", "org/acme/app/1.0/app-1.0.jar", "org/acme/app/maven-metadata.xml"} {
		remoteStatus(t, w.request("GET", "/artifactory/remote/"+p, "reader", ""), 200)
	}
	remoteStatus(
		t,
		w.request("GET", "/artifactory/remote/org/acme/app/1.0/app-1.0.jar.sha1", "reader", ""),
		200,
	)
	items := &item.Service{Store: w.rt.store, Blobs: w.rt.blobs}
	if err := items.Reindex(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	if _, err := w.rt.store.Items().
		Get(t.Context(), w.c.Key, true, "org/acme/app/maven-metadata.xml"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("source metadata became a derived item", err)
	}
	w.up.Close()
	for _, key := range []string{"remote", "remote-cache"} {
		res := w.request(
			"GET",
			"/artifactory/"+key+"/org/acme/app/maven-metadata.xml",
			"reader",
			"",
		)
		remoteStatus(t, res, 200)
		if res.Body.String() != metadata {
			t.Fatal("metadata reduced to cached subset", res.Body)
		}
		res = w.request("GET", "/artifactory/"+key+"/org/acme/app/1.0/app-1.0.jar", "reader", "")
		remoteStatus(t, res, 200)
		if !bytes.Equal(res.Body.Bytes(), jar) {
			t.Fatal("release bytes changed")
		}
	}
}

func TestAccMavenRemoteSnapshotAndReleaseResolution(t *testing.T) {
	w := newMavenRemoteWorld(t)
	unique := "org/acme/app/1.1-SNAPSHOT/app-1.1-20261021.120000-1.jar"
	mavenSource(w, unique, sourceJAR(t, "unique"), "application/java-archive")
	mavenSource(
		w,
		"org/acme/app/1.1-SNAPSHOT/maven-metadata.xml",
		[]byte(
			`<metadata><versioning><snapshotVersions><snapshotVersion><extension>jar</extension><value>1.1-20261021.120000-1</value></snapshotVersion></snapshotVersions></versioning></metadata>`,
		),
		"application/xml",
	)
	remoteStatus(
		t,
		w.request(
			"GET",
			"/artifactory/remote/org/acme/app/1.1-SNAPSHOT/app-1.1-SNAPSHOT.jar",
			"reader",
			"",
		),
		200,
	)
	if w.up.Count("GET", "/"+unique) != 1 ||
		w.up.Count("GET", "/org/acme/app/1.1-SNAPSHOT/app-1.1-SNAPSHOT.jar") != 0 {
		t.Fatal(w.up.Requests())
	}
	mavenSource(
		w,
		"org/acme/legacy/1.0-SNAPSHOT/maven-metadata.xml",
		[]byte(
			`<metadata><versioning><snapshot><timestamp>20261021.120000</timestamp><buildNumber>2</buildNumber></snapshot></versioning></metadata>`,
		),
		"application/xml",
	)
	mavenSource(
		w,
		"org/acme/legacy/1.0-SNAPSHOT/legacy-1.0-20261021.120000-2.jar",
		sourceJAR(t, "legacy"),
		"application/java-archive",
	)
	remoteStatus(
		t,
		w.request(
			"GET",
			"/artifactory/remote/org/acme/legacy/1.0-SNAPSHOT/legacy-1.0-SNAPSHOT.jar",
			"reader",
			"",
		),
		200,
	)
	nonunique := "org/acme/old/1.0-SNAPSHOT/old-1.0-SNAPSHOT.jar"
	mavenSource(w, nonunique, sourceJAR(t, "nonunique"), "application/java-archive")
	remoteStatus(t, w.request("GET", "/artifactory/remote/"+nonunique, "reader", ""), 200)
	if _, err := w.rt.store.Items().Get(t.Context(), w.c.Key, true, nonunique); err != nil {
		t.Fatal("mutable binary not stored as item", err)
	}
	mavenSource(
		w,
		"org/acme/app/maven-metadata.xml",
		[]byte(
			`<metadata><versioning><latest>2.0</latest><release>2.0</release><versions><version>2.0</version></versions></versioning></metadata>`,
		),
		"application/xml",
	)
	mavenSource(
		w,
		"org/acme/app/2.0/app-2.0.jar",
		sourceJAR(t, "latest"),
		"application/java-archive",
	)
	remoteStatus(
		t,
		w.request(
			"GET",
			"/artifactory/remote/org/acme/app/[RELEASE]/app-[RELEASE].jar",
			"reader",
			"",
		),
		200,
	)
	remoteStatus(
		t,
		w.request(
			"GET",
			"/artifactory/remote/org/acme/app/[LATEST]/app-[LATEST].jar",
			"reader",
			"",
		),
		200,
	)
}

func TestAccMavenRemoteDenialsAndAdmission(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint(noStore), func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			w.c.RejectInvalidJars, w.c.StoreArtifactsLocally = new(true), new(!noStore)
			w.update()
			for _, p := range []string{"org/acme/bad/1.0/bad-1.0.jar", "org/acme/bad/1.0/bad-1.0.pom", "org/acme/bad/maven-metadata.xml"} {
				w.up.Set(
					"/"+p,
					fakeupstream.Resource{
						Body:   []byte("invalid"),
						Header: http.Header{"Content-Type": {"application/octet-stream"}},
					},
				)
				remoteStatus(t, w.request("GET", "/artifactory/remote/"+p, "reader", ""), 502)
				if _, err := w.rt.store.Items().
					Get(t.Context(), w.c.Key, true, p); !errors.Is(
					err,
					store.ErrNotFound,
				) {
					t.Fatal("invalid source published", err)
				}
			}
		})
	}
	w := newMavenRemoteWorld(t)
	w.c.HandleReleases, w.c.HandleSnapshots = new(false), new(false)
	w.update()
	for _, p := range []string{"org/acme/app/1.0/app-1.0.jar", "org/acme/app/1.1-SNAPSHOT/app-1.1-SNAPSHOT.jar"} {
		remoteStatus(t, w.request("GET", "/artifactory/remote/"+p, "reader", ""), 409)
	}
	if w.up.Count("", "") != 0 {
		t.Fatal("known policy denial reached upstream", w.up.Requests())
	}
	w.c.HandleReleases, w.c.HandleSnapshots = new(true), new(true)
	w.c.ExcludesPattern = new("org/acme/private/**")
	w.update()
	remoteStatus(
		t,
		w.request("GET", "/artifactory/remote/org/acme/private/1.0/private-1.0.jar", "reader", ""),
		404,
	)
	remoteStatus(
		t,
		w.request("GET", "/artifactory/remote/org/acme/app/1.0/app-1.0.jar", "denied", ""),
		403,
	)
	remoteStatus(
		t,
		w.request(
			"GET",
			"/artifactory/remote/org/acme/private/1.0-SNAPSHOT/private-1.0-SNAPSHOT.jar",
			"reader",
			"",
		),
		404,
	)
	if w.up.Count("", "") != 0 {
		t.Fatal("permission/pattern denial reached upstream")
	}
}

func TestAccMavenRemoteChecksumPolicies(t *testing.T) {
	for _, policy := range []repo.RemoteChecksumPolicy{repo.GenerateIfAbsent, repo.Fail, repo.IgnoreAndGenerate, repo.PassThru} {
		t.Run(string(policy), func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			w.c.RemoteRepoChecksumPolicyType = policy
			w.update()
			p := "org/acme/app/1.0/app-1.0.jar"
			w.up.Set(
				"/"+p,
				fakeupstream.Resource{
					Body:   sourceJAR(t, "valid"),
					Header: http.Header{"Content-Type": {"application/java-archive"}},
				},
			)
			wrong := strings.Repeat("0", 40)
			w.up.Set("/"+p+".sha1", fakeupstream.Resource{Body: []byte(wrong)})
			want := 502
			if policy == repo.IgnoreAndGenerate || policy == repo.PassThru {
				want = 200
			}
			remoteStatus(t, w.request("GET", "/artifactory/remote/"+p, "reader", ""), want)
			if policy == repo.PassThru {
				res := w.request("GET", "/artifactory/remote/"+p+".sha1", "reader", "")
				remoteStatus(t, res, 200)
				if res.Body.String() != wrong {
					t.Fatal("lost original pass-thru checksum")
				}
			}
			if policy == repo.IgnoreAndGenerate && w.up.Count("GET", "/"+p+".sha1") != 0 {
				t.Fatal("ignored checksum fetched")
			}
		})
	}
}

func TestAccMavenRemoteEagerCompanionsAndRetention(t *testing.T) {
	w := newMavenRemoteWorld(t)
	w.c.FetchJarsEagerly, w.c.FetchSourcesEagerly, w.c.MaxUniqueSnapshots = new(
		true,
	), new(
		true,
	), new(
		2,
	)
	w.update()
	base := "org/acme/app/1.0/"
	mavenSource(w, base+"app-1.0.pom", []byte(sourcePOM("app", "1.0")), "application/xml")
	mavenSource(w, base+"app-1.0.jar", sourceJAR(t, "main"), "application/java-archive")
	mavenSource(w, base+"app-1.0-sources.jar", sourceJAR(t, "sources"), "application/java-archive")
	remoteStatus(t, w.request("GET", "/artifactory/remote/"+base+"app-1.0.pom", "reader", ""), 200)
	waitMavenRequest(t, w, "/"+base+"app-1.0.jar")
	remoteStatus(t, w.request("GET", "/artifactory/remote/"+base+"app-1.0.jar", "reader", ""), 200)
	waitMavenRequest(t, w, "/"+base+"app-1.0-sources.jar")
	for i := 1; i <= 3; i++ {
		p := fmt.Sprintf("org/acme/snap/1.0-SNAPSHOT/snap-1.0-20261021.12000%d-%d.jar", i, i)
		mavenSource(w, p, sourceJAR(t, fmt.Sprint(i)), "application/java-archive")
		remoteStatus(t, w.request("GET", "/artifactory/remote/"+p, "reader", ""), 200)
	}
	items := &item.Service{Store: w.rt.store, Blobs: w.rt.blobs}
	if err := (maven.Handler{}).Work(
		t.Context(),
		items.Env(),
		types.WorkItem{Kind: maven.KindSnapshots, Key: "remote/org.acme:snap"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.rt.store.Items().
		Get(t.Context(), w.c.Key, true, "org/acme/snap/1.0-SNAPSHOT/snap-1.0-20261021.120001-1.jar"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("old remote snapshot retained", err)
	}
}

func waitMavenRequest(t *testing.T, w *remoteWorld, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for w.up.Count("GET", path) != 1 {
		select {
		case <-ctx.Done():
			t.Fatal("companion not fetched", path, w.up.Requests())
		case <-tick.C:
		}
	}
}

func TestAccMavenFamilyLayoutRequests(t *testing.T) {
	for _, tc := range []struct {
		kind                              repo.PackageType
		layout, upstream, request, origin string
	}{
		{repo.Gradle, "gradle-default", "", "org.acme/app/1.0/app-1.0.jar", "org.acme/app/1.0/app-1.0.jar"},
		{repo.Ivy, "ivy-default", "", "org.acme/app/1.0/jars/app-1.0.jar", "org.acme/app/1.0/jars/app-1.0.jar"},
		{repo.SBT, "sbt-default", "", "org.acme/app/scala_2.13/sbt_1.0/1.0/jars/app.jar", "org.acme/app/scala_2.13/sbt_1.0/1.0/jars/app.jar"},
		{repo.Gradle, "gradle-default", "maven-2-default", "org.acme/app/1.0/app-1.0.jar", "org/acme/app/1.0/app-1.0.jar"},
	} {
		t.Run(string(tc.kind)+tc.upstream, func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			w.c.PackageType, w.c.RepoLayoutRef, w.c.RemoteRepoLayoutRef = tc.kind, tc.layout, tc.upstream
			w.update()
			jar := sourceJAR(t, "layout fixture")
			mavenSource(w, tc.origin, jar, "application/java-archive")
			res := w.request("GET", "/artifactory/remote/"+tc.request, "reader", "")
			remoteStatus(t, res, 200)
			if !bytes.Equal(res.Body.Bytes(), jar) || w.up.Count("GET", "/"+tc.origin) != 1 {
				t.Fatal("layout resolution", w.up.Requests())
			}
			if _, err := w.rt.store.Items().
				Get(t.Context(), w.c.Key, true, tc.request); err != nil {
				t.Fatal("canonical cache path lost", err)
			}
			items := &item.Service{Store: w.rt.store, Blobs: w.rt.blobs}
			if err := items.Reindex(t.Context(), w.c); err != nil {
				t.Fatal(err)
			}
			pkg, err := w.rt.store.Packages().Find(t.Context(), w.c.Key, "org.acme:app")
			if err != nil || pkg.ID == 0 {
				t.Fatal("fetched content not indexed", err)
			}
		})
	}
	w := newMavenRemoteWorld(t)
	w.c.RemoteRepoLayoutRef = "sbt-default"
	w.update()
	remoteStatus(
		t,
		w.request("GET", "/artifactory/remote/org/acme/app/1.0/app-1.0.jar", "reader", ""),
		400,
	)
	if w.up.Count("", "") != 0 {
		t.Fatal("unrepresentable mapping fetched")
	}
}

func TestAccMavenCompanionSurvivesRuntimeRestart(t *testing.T) {
	cfg := testConfig(t)
	cfg.Bootstrap.AdminPassword = "pw"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := openRuntime(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	up := fakeupstream.New(t, fakeupstream.Options{})
	c := *repo.Defaults(&repo.Config{Key: "maven", Class: repo.Remote, PackageType: repo.Maven, URL: up.URL, FetchJarsEagerly: new(true)})
	if err = rt.store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	p := "org/acme/app/1.0/app-1.0"
	up.Set(
		"/"+p+".pom",
		fakeupstream.Resource{
			Body:   []byte(sourcePOM("app", "1.0")),
			Header: http.Header{"Content-Type": {"application/xml"}},
		},
	)
	up.Set("/"+p+".jar", fakeupstream.Resource{Body: sourceJAR(t, "durable")})
	rt.mavenEager.Close()
	h := server.New(
		server.Config{
			Store:         rt.store,
			Blobs:         rt.blobs,
			Auth:          rt.auth,
			RemoteSources: rt.remoteSources,
			Logger:        log,
		},
	)
	r := httptest.NewRequest("GET", "/artifactory/maven/"+p+".pom", nil)
	r.SetBasicAuth("admin", "pw")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, r)
	remoteStatus(t, response, 200)
	stats, err := rt.store.WorkQueue().Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, stat := range stats {
		if stat.Kind == remote.MavenCompanionKind && stat.Pending == 1 {
			found = true
		}
	}
	if !found || up.Count("GET", "/"+p+".jar") != 0 {
		t.Fatal("companion not durably pending", stats)
	}
	if err = rt.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	rt, err = openRuntime(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err = rt.store.Items().Get(deadline, "maven", true, p+".jar"); err == nil {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("restart did not resume companion", err)
		case <-tick.C:
		}
	}
	if up.Count("GET", "/"+p+".jar") != 1 {
		t.Fatal(up.Requests())
	}
}

func TestAccMavenPassThruChecksumsForTransientAndMetadata(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint(noStore), func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			w.c.RemoteRepoChecksumPolicyType = repo.PassThru
			w.c.StoreArtifactsLocally = new(!noStore)
			w.update()
			for _, p := range []string{"org/acme/app/1.0/app-1.0.jar", "org/acme/app/maven-metadata.xml"} {
				data := []byte("artifact")
				contentType := "application/octet-stream"
				if strings.HasSuffix(p, ".xml") {
					data = []byte("<metadata/>")
					contentType = "application/xml"
				}
				w.up.Set(
					"/"+p,
					fakeupstream.Resource{
						Body:   data,
						Header: http.Header{"Content-Type": {contentType}},
					},
				)
				for _, alg := range []item.Algorithm{item.SHA1, item.MD5, item.SHA512} {
					n := 40
					if alg == item.MD5 {
						n = 32
					}
					if alg == item.SHA512 {
						n = 128
					}
					wrong := strings.Repeat("0", n)
					w.up.Set("/"+p+"."+string(alg), fakeupstream.Resource{Body: []byte(wrong)})
				}
				remoteStatus(t, w.request("GET", "/artifactory/remote/"+p, "reader", ""), 200)
				for _, alg := range []item.Algorithm{item.SHA1, item.MD5, item.SHA256, item.SHA512} {
					result := w.request(
						"GET",
						"/artifactory/remote/"+p+"."+string(alg),
						"reader",
						"",
					)
					remoteStatus(t, result, 200)
					if alg == item.SHA256 {
						if len(result.Body.String()) != 64 ||
							result.Body.String() == strings.Repeat("0", 64) {
							t.Fatal("missing checksum not generated", result.Body)
						}
					} else if strings.Trim(result.Body.String(), "0") != "" {
						t.Fatal("pass-thru claim lost", alg, result.Body)
					}
				}
			}
		})
	}
}

func TestAccMavenColdChecksumHeadDoesNotAdmitOwner(t *testing.T) {
	w := newMavenRemoteWorld(t)
	p := "org/acme/app/1.0/app-1.0.jar"
	w.up.Set("/"+p, fakeupstream.Resource{Body: []byte("artifact")})
	for _, tc := range []struct{ algorithm, length string }{{"md5", "32"}, {"sha1", "40"}, {"sha256", "64"}, {"sha512", "128"}} {
		result := w.request("HEAD", "/artifactory/remote/"+p+"."+tc.algorithm, "reader", "")
		remoteStatus(t, result, 200)
		if result.Body.Len() != 0 || result.Header().Get("Content-Length") != tc.length {
			t.Fatal(result.Header(), result.Body)
		}
	}
	if w.up.Count("GET", "") != 0 {
		t.Fatal("HEAD fetched body", w.up.Requests())
	}
	if _, err := w.rt.store.Items().
		Get(t.Context(), w.c.Key, true, p); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("HEAD admitted owner", err)
	}
}

func TestAccMavenUpstreamNoStoreDoesNotScheduleCompanion(t *testing.T) {
	w := newMavenRemoteWorld(t)
	w.c.FetchJarsEagerly = new(true)
	w.update()
	p := "org/acme/app/1.0/app-1.0"
	w.up.Set(
		"/"+p+".pom",
		fakeupstream.Resource{
			Body:   []byte(sourcePOM("app", "1.0")),
			Header: http.Header{"Content-Type": {"application/xml"}, "Cache-Control": {"no-store"}},
		},
	)
	w.up.Set("/"+p+".jar", fakeupstream.Resource{Body: sourceJAR(t, "must not fetch")})
	remoteStatus(t, w.request("GET", "/artifactory/remote/"+p+".pom", "reader", ""), 200)
	stats, err := w.rt.store.WorkQueue().Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, stat := range stats {
		if stat.Kind == remote.MavenCompanionKind && stat.Pending+stat.Leased+stat.Parked > 0 {
			t.Fatal("no-store response scheduled companion", stat)
		}
	}
	if w.up.Count("GET", "/"+p+".jar") != 0 {
		t.Fatal("no-store response fetched companion")
	}
}
