package maven_test

import (
	"context"
	"crypto/sha1" //nolint:gosec // Maven checksum wire protocol
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// declaredChecksums are the X-Checksum-* values an upstream declares for
// a file in these tests. None is the file's own digest, so an answer that
// carries one came from the declaration, not from the bytes.
var declaredChecksums = map[string]struct{ header, value string }{
	"md5":    {"X-Checksum-Md5", strings.Repeat("a", 32)},
	"sha1":   {"X-Checksum-Sha1", strings.Repeat("b", 40)},
	"sha256": {"X-Checksum-Sha256", strings.Repeat("c", 64)},
	"sha512": {"X-Checksum-Sha512", strings.Repeat("d", 128)},
}

// checksumNotFound is the body 7.x's respondForChecksumRequest answers
// when the checksum policy finds no value for file.
func checksumNotFound(file string) string {
	return `{"errors":[{"message":"Checksum not found for ` + file + `","status":404}]}` + "\n"
}

// wantNothingKept fails t when the upstream was asked for anything but a
// HEAD, or the cache holds the file at path.
func wantNothingKept(t *testing.T, w *remoteWorld, path string) {
	t.Helper()
	if n := w.up.Count("GET", ""); n != 0 {
		t.Fatal("checksum request downloaded", w.up.Requests())
	}
	if _, err := w.rt.Store.Items().
		Get(t.Context(), w.c.Key, true, path); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatal("checksum request kept the file", err)
	}
}

// TestAccMavenColdChecksumAnswersFromFileInfo (#1476): a checksum request
// for a file a Maven remote that stores artifacts locally does not hold
// is answered from the file's info, the upstream's answer to one HEAD,
// through the remote's checksum policy, and downloads and keeps nothing,
// HEAD or GET. 7.161.26's DownloadServiceImpl.respond hands it to
// respondForChecksumRequest; RemoteRepoBase.getChecksum reads the info's
// checksums, which hold only the upstream's declarations as originals,
// through the policy: pass-thru answers the original, the other three the
// actual value a file never downloaded lacks, so they answer 404.
func TestAccMavenColdChecksumAnswersFromFileInfo(t *testing.T) {
	const p = "org/acme/app/1.0/app-1.0.jar"
	for _, policy := range []repo.RemoteChecksumPolicy{repo.GenerateIfAbsent, repo.Fail, repo.IgnoreAndGenerate, repo.PassThru} {
		for _, declares := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/declared=%v", policy, declares), func(t *testing.T) {
				w := newMavenRemoteWorld(t)
				w.c.RemoteRepoChecksumPolicyType = policy
				w.update()
				header := http.Header{"Content-Type": {"application/java-archive"}}
				if declares {
					for _, d := range declaredChecksums {
						header.Set(d.header, d.value)
					}
				}
				modified := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
				w.up.Set(
					"/"+p,
					fakeupstream.Resource{
						Body:     sourceJAR(t, "cold"),
						Header:   header,
						Modified: modified,
					},
				)
				for _, method := range []string{"GET", "HEAD"} {
					for alg, d := range declaredChecksums {
						res := w.request(method, "/artifactory/remote/"+p+"."+alg, "reader", "")
						if policy != repo.PassThru || !declares {
							remoteStatus(t, res, 404)
							if method == "GET" && res.Body.String() != checksumNotFound(p) {
								t.Fatal(method, alg, res.Body)
							}
							continue
						}
						remoteStatus(t, res, 200)
						h := res.Header()
						if h.Get("Content-Length") != strconv.Itoa(len(d.value)) ||
							method == "GET" && res.Body.String() != d.value ||
							method == "HEAD" && (res.Body.Len() != 0 ||
								h.Get("ETag") != declaredChecksums["sha1"].value ||
								h.Get("Last-Modified") != modified.Format(http.TimeFormat)) {
							t.Fatal(method, alg, h, res.Body)
						}
					}
				}
				if n := w.up.Count("HEAD", "/"+p); n != 8 {
					t.Fatal("want one upstream HEAD a request", n, w.up.Requests())
				}
				wantNothingKept(t, w, p)
			})
		}
	}
}

// TestAccMavenColdChecksumConditionals: the conditional answers #1459
// settled for checksum files hold for a cold one read from the file's
// info. A checksum the policy answers evaluates If-None-Match against the
// SHA-1 the upstream declared and answers 304 with the file's headers; a
// checksum the policy cannot answer is 404 before any conditional header
// is read, as a local checksum file is.
func TestAccMavenColdChecksumConditionals(t *testing.T) {
	const p = "org/acme/app/1.0/app-1.0.jar"
	sha1Value := declaredChecksums["sha1"].value
	for _, policy := range []repo.RemoteChecksumPolicy{repo.GenerateIfAbsent, repo.PassThru} {
		t.Run(string(policy), func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			w.c.RemoteRepoChecksumPolicyType = policy
			w.update()
			w.up.Set("/"+p, fakeupstream.Resource{
				Body: sourceJAR(t, "cold"),
				Header: http.Header{
					"Content-Type":    {"application/java-archive"},
					"X-Checksum-Sha1": {sha1Value},
				},
			})
			for _, method := range []string{"GET", "HEAD"} {
				for _, tag := range []string{sha1Value, `"` + sha1Value + `"`, `W/"` + sha1Value + `"`} {
					res := w.request(
						method,
						"/artifactory/remote/"+p+".sha1",
						"reader",
						"",
						"If-None-Match",
						tag,
					)
					if policy != repo.PassThru {
						remoteStatus(t, res, 404)
						continue
					}
					remoteStatus(t, res, 304)
					if res.Header().Get("ETag") != sha1Value ||
						res.Header().Get("X-Checksum-Sha1") != sha1Value {
						t.Fatal(method, tag, res.Header())
					}
				}
				res := w.request(
					method,
					"/artifactory/remote/"+p+".sha1",
					"reader",
					"",
					"If-Match",
					`"other"`,
				)
				if policy == repo.PassThru {
					remoteStatus(t, res, 200)
				} else {
					remoteStatus(t, res, 404)
				}
			}
			wantNothingKept(t, w, p)
		})
	}
}

// TestAccMavenWarmChecksumUnchanged: a checksum request for a file the
// cache holds answers from the kept copy, as before #1476, and asks the
// upstream nothing. A cached maven-metadata.xml, which the cache keeps as
// a document without an item, is held too; a cold one answers 404 under
// generate-if-absent and is not fetched.
func TestAccMavenWarmChecksumUnchanged(t *testing.T) {
	w := newMavenRemoteWorld(t)
	jar := "org/acme/app/1.0/app-1.0.jar"
	body := sourceJAR(t, "warm")
	mavenSource(w, jar, body, "application/java-archive")
	metadata := "org/acme/app/maven-metadata.xml"
	document := []byte(`<metadata><versioning><release>1.0</release></versioning></metadata>`)
	mavenSource(w, metadata, document, "application/xml")

	res := w.request("GET", "/artifactory/remote/"+metadata+".sha1", "reader", "")
	remoteStatus(t, res, 404)
	if res.Body.String() != checksumNotFound(metadata) || w.up.Count("GET", "") != 0 {
		t.Fatal(res.Body, w.up.Requests())
	}

	for p, data := range map[string][]byte{jar: body, metadata: document} {
		remoteStatus(t, w.request("GET", "/artifactory/remote/"+p, "reader", ""), 200)
		before := len(w.up.Requests())
		sum := sha1.Sum(data) //nolint:gosec // Maven checksum wire protocol
		want := hex.EncodeToString(sum[:])
		for _, method := range []string{"GET", "HEAD"} {
			res := w.request(method, "/artifactory/remote/"+p+".sha1", "reader", "")
			remoteStatus(t, res, 200)
			if method == "GET" && res.Body.String() != want ||
				res.Header().Get("Content-Length") != "40" {
				t.Fatal(p, method, res.Header(), res.Body)
			}
		}
		if after := len(w.up.Requests()); after != before {
			t.Fatal("warm checksum asked the upstream", w.up.Requests()[before:])
		}
	}
}

// TestAccMavenFamilyColdChecksum: Gradle, Ivy and sbt remotes answer a
// cold checksum request as a Maven remote does, from their own layouts.
func TestAccMavenFamilyColdChecksum(t *testing.T) {
	for _, tc := range []struct {
		kind         repo.PackageType
		layout, path string
	}{
		{repo.Gradle, "gradle-default", "org.acme/app/1.0/app-1.0.jar"},
		{repo.Ivy, "ivy-default", "org.acme/app/1.0/jars/app-1.0.jar"},
		{repo.SBT, "sbt-default", "org.acme/app/scala_2.13/sbt_1.0/1.0/jars/app.jar"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			w.c.PackageType, w.c.RepoLayoutRef = tc.kind, tc.layout
			w.update()
			mavenSource(w, tc.path, sourceJAR(t, "family"), "application/java-archive")
			for _, method := range []string{"GET", "HEAD"} {
				res := w.request(method, "/artifactory/remote/"+tc.path+".sha1", "reader", "")
				remoteStatus(t, res, 404)
				if method == "GET" && res.Body.String() != checksumNotFound(tc.path) {
					t.Fatal(res.Body)
				}
			}
			wantNothingKept(t, w, tc.path)
		})
	}
}

// TestAccMavenResolvedSnapshotChecksumHeld: a checksum request for a
// non-unique snapshot is read from the unique build its metadata names;
// once the cache holds that build its checksum answers from it.
func TestAccMavenResolvedSnapshotChecksumHeld(t *testing.T) {
	w := newMavenRemoteWorld(t)
	unique := "org/acme/app/1.1-SNAPSHOT/app-1.1-20261021.120000-1.jar"
	body := sourceJAR(t, "unique")
	mavenSource(w, unique, body, "application/java-archive")
	mavenSource(
		w,
		"org/acme/app/1.1-SNAPSHOT/maven-metadata.xml",
		[]byte(
			`<metadata><versioning><snapshotVersions><snapshotVersion><extension>jar</extension><value>1.1-20261021.120000-1</value></snapshotVersion></snapshotVersions></versioning></metadata>`,
		),
		"application/xml",
	)
	nonunique := "/artifactory/remote/org/acme/app/1.1-SNAPSHOT/app-1.1-SNAPSHOT.jar"
	res := w.request("GET", nonunique+".sha1", "reader", "")
	remoteStatus(t, res, 404)
	if res.Body.String() != checksumNotFound(unique) || w.up.Count("GET", "/"+unique) != 0 {
		t.Fatal(res.Body, w.up.Requests())
	}
	remoteStatus(t, w.request("GET", nonunique, "reader", ""), 200)
	res = w.request("GET", nonunique+".sha1", "reader", "")
	remoteStatus(t, res, 200)
	sum := sha1.Sum(body) //nolint:gosec // Maven checksum wire protocol
	if res.Body.String() != hex.EncodeToString(sum[:]) || w.up.Count("GET", "/"+unique) != 1 {
		t.Fatal(res.Body, w.up.Requests())
	}
}

// heldFaultStore fails the item and remote cache lookups that tell
// whether the cache holds a file, at one path.
type heldFaultStore struct {
	harness.Store
	items, documents string
}

func (s heldFaultStore) Items() port.ItemStore {
	return heldFaultItems{ItemStore: s.Store.Items(), path: s.items}
}

func (s heldFaultStore) RemoteCache() port.RemoteCacheStore {
	return heldFaultCache{RemoteCacheStore: s.Store.RemoteCache(), path: s.documents}
}

type heldFaultItems struct {
	port.ItemStore
	path string
}

var errHeldFault = errors.New("store fault")

//nolint:wrapcheck // test double
func (i heldFaultItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (artmodel.Item, error) {
	if p == i.path {
		return artmodel.Item{}, errHeldFault
	}
	return i.ItemStore.Get(ctx, key, cached, p)
}

type heldFaultCache struct {
	port.RemoteCacheStore
	path string
}

//nolint:wrapcheck // test double
func (c heldFaultCache) Exact(
	ctx context.Context, key, p string, page kernel.Page,
) ([]artmodel.RemoteCacheEntry, string, error) {
	if p == c.path {
		return nil, "", errHeldFault
	}
	return c.RemoteCacheStore.Exact(ctx, key, p, page)
}

// TestAccMavenColdChecksumStoreFault: a failed read of the cache while the
// route decides whether it holds the file is the server's fault, 500, and
// the upstream is not asked.
func TestAccMavenColdChecksumStoreFault(t *testing.T) {
	const p = "org/acme/app/1.0/app-1.0.jar"
	for _, fault := range []heldFaultStore{{items: p}, {documents: p}} {
		t.Run(fmt.Sprintf("items=%v", fault.items != ""), func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			fault.Store = w.rt.Store
			w.h = harness.Router(harness.Config{
				Store:         fault,
				Blobs:         w.rt.Blobs,
				Auth:          w.rt.Auth,
				Tokens:        w.rt.Tokens,
				Keys:          w.rt.Keys,
				RemoteSources: w.rt.Artifacts.RemoteSources(),
				RemoteHTTP:    w.rt.Artifacts.RemoteHTTP(),
				Logger:        slog.New(slog.DiscardHandler),
			})
			mavenSource(w, p, sourceJAR(t, "fault"), "application/java-archive")
			res := w.request("GET", "/artifactory/remote/"+p+".sha1", "reader", "")
			remoteStatus(t, res, 500)
			if w.up.Count("", "") != 0 {
				t.Fatal(w.up.Requests())
			}
		})
	}
}

// noStoreWorld is a Maven remote that stores nothing, under policy, over
// an upstream holding the jar at p, which declares declaredChecksums.
func noStoreWorld(t *testing.T, policy repo.RemoteChecksumPolicy, p string) *remoteWorld {
	t.Helper()
	w := newMavenRemoteWorld(t)
	w.c.StoreArtifactsLocally = new(false)
	w.c.RemoteRepoChecksumPolicyType = policy
	w.update()
	header := http.Header{"Content-Type": {"application/java-archive"}}
	for _, d := range declaredChecksums {
		header.Set(d.header, d.value)
	}
	w.up.Set("/"+p, fakeupstream.Resource{
		Body:     sourceJAR(t, "no-store"),
		Header:   header,
		Modified: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	})
	return w
}

// TestAccMavenNoStoreChecksumFromUpstreamFile: a Maven remote that stores
// nothing answers a checksum request, HEAD or GET, under every checksum
// policy, with the upstream's own checksum file: one HEAD of the file,
// whose body is never asked for, then one GET of <file>.<alg>, answered
// with its first token. 7.161.26's RemoteRepoBase.getChecksum calls
// getRemoteChecksum for a remote that stores nothing, which for the Maven
// family downloads the checksum path (getRemoteChecksumFromPath) and reads
// it with ChecksumUtils.checksumStringFromStream; the upstream's 404 is
// null (handleRemoteRequestException), and respondForChecksumRequest
// answers 404 "Checksum not found for <file>".
func TestAccMavenNoStoreChecksumFromUpstreamFile(t *testing.T) {
	const p = "org/acme/app/1.0/app-1.0.jar"
	upstreamSums := map[string]string{
		"md5":    strings.Repeat("1", 32),
		"sha1":   strings.Repeat("2", 40),
		"sha256": strings.Repeat("3", 64),
		"sha512": strings.Repeat("4", 128),
	}
	for _, policy := range []repo.RemoteChecksumPolicy{repo.GenerateIfAbsent, repo.Fail, repo.IgnoreAndGenerate, repo.PassThru} {
		for _, present := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/checksum-file=%v", policy, present), func(t *testing.T) {
				w := noStoreWorld(t, policy, p)
				if present {
					for alg, sum := range upstreamSums {
						w.up.Set("/"+p+"."+alg, fakeupstream.Resource{
							Body:   []byte(sum + "  app-1.0.jar\n"),
							Header: http.Header{"Content-Type": {"text/plain"}},
						})
					}
				}
				for _, method := range []string{"GET", "HEAD"} {
					for alg, sum := range upstreamSums {
						res := w.request(method, "/artifactory/remote/"+p+"."+alg, "reader", "")
						if !present {
							remoteStatus(t, res, 404)
							if method == "GET" && res.Body.String() != checksumNotFound(p) {
								t.Fatal(method, alg, res.Body)
							}
							continue
						}
						remoteStatus(t, res, 200)
						h := res.Header()
						if h.Get("Content-Length") != strconv.Itoa(len(sum)) ||
							method == "GET" && res.Body.String() != sum ||
							method == "HEAD" && (res.Body.Len() != 0 ||
								h.Get("ETag") != declaredChecksums["sha1"].value) {
							t.Fatal(method, alg, h, res.Body)
						}
					}
				}
				if n := w.up.Count("GET", "/"+p); n != 0 {
					t.Fatal("checksum request asked for the file's body", w.up.Requests())
				}
				if n := w.up.Count("HEAD", "/"+p); n != 8 {
					t.Fatal("want one upstream HEAD of the file a request", n, w.up.Requests())
				}
				// The checksum file's GET follows a HEAD of it, as every GET
				// this server sends upstream does unless bypassHeadRequests,
				// and no checksum of the checksum file is asked for.
				gets := 0
				if present {
					gets = 2
				}
				for alg := range upstreamSums {
					if n := w.up.Count("GET", "/"+p+"."+alg); n != gets {
						t.Fatal("want one upstream GET of the checksum file a request", alg, n)
					}
				}
				if n := w.up.Count("GET", ""); n != gets*len(upstreamSums) {
					t.Fatal("asked the upstream for more than the checksum files", w.up.Requests())
				}
			})
		}
	}
}

// TestAccMavenNoStoreChecksumFileForms: the checksum file is read as
// ChecksumUtils.checksumStringFromStream reads it: blank lines and lines
// starting with "//" are skipped, an "MD5 (name)= <sum>" line answers
// <sum>, the first token answers, unchecked against the algorithm, and a
// file with no such line is not found.
func TestAccMavenNoStoreChecksumFileForms(t *testing.T) {
	const p = "org/acme/app/1.0/app-1.0.jar"
	for _, tc := range []struct{ name, body, want string }{
		{"bare", "0123abcd", "0123abcd"},
		{"named", "0123abcd *app-1.0.jar\r\n", "0123abcd"},
		{"comments", "\n   \n// generated\n  0123abcd  \n", "0123abcd"},
		{"bsd", "MD5 (app-1.0.jar)= 0123abcd\n", "0123abcd"},
		{"unchecked", "not-a-checksum trailing", "not-a-checksum"},
		{"blank", "\n\n// only a comment\n", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := noStoreWorld(t, repo.GenerateIfAbsent, p)
			w.up.Set("/"+p+".md5", fakeupstream.Resource{Body: []byte(tc.body)})
			res := w.request("GET", "/artifactory/remote/"+p+".md5", "reader", "")
			if tc.want == "" {
				remoteStatus(t, res, 404)
				if res.Body.String() != checksumNotFound(p) {
					t.Fatal(res.Body)
				}
				return
			}
			remoteStatus(t, res, 200)
			if res.Body.String() != tc.want {
				t.Fatalf("%q, want %q", res.Body, tc.want)
			}
		})
	}
}

// TestAccMavenNoStoreChecksumNeedsTheFile: a checksum request for a file
// the upstream does not have answers as the file does and never asks for
// the checksum file, as 7.x's respond answers a resource it did not find
// before its checksum branch. An upstream fault on the checksum file is
// the upstream's fault, not a missing checksum; a refused label is
// refused, as HttpRepo.downloadResource's blockMismatchingMimeTypes
// refuses it.
func TestAccMavenNoStoreChecksumNeedsTheFile(t *testing.T) {
	const p = "org/acme/app/1.0/app-1.0.jar"
	w := noStoreWorld(t, repo.GenerateIfAbsent, p)
	w.up.Set("/org/acme/app/1.0/gone-1.0.jar.sha1", fakeupstream.Resource{Body: []byte("0123abcd")})
	for _, method := range []string{"GET", "HEAD"} {
		res := w.request(
			method,
			"/artifactory/remote/org/acme/app/1.0/gone-1.0.jar.sha1",
			"reader",
			"",
		)
		remoteStatus(t, res, 404)
	}
	if n := w.up.Count("GET", ""); n != 0 {
		t.Fatal("checksum file asked for without its file", w.up.Requests())
	}

	w.up.Set("/"+p+".sha1", fakeupstream.Resource{Status: http.StatusServiceUnavailable})
	fault := w.request("GET", "/artifactory/remote/"+p+".sha1", "reader", "")
	file := newMavenRemoteWorld(t)
	file.c.StoreArtifactsLocally = new(false)
	file.update()
	file.up.Set("/"+p, fakeupstream.Resource{Status: http.StatusServiceUnavailable})
	remoteStatus(t, fault, file.request("GET", "/artifactory/remote/"+p, "reader", "").Code)

	w.up.Set("/"+p+".sha1", fakeupstream.Resource{
		Body:   []byte("<html>0123abcd</html>"),
		Header: http.Header{"Content-Type": {"text/html"}},
	})
	res := w.request("GET", "/artifactory/remote/"+p+".sha1", "reader", "")
	if res.Code == http.StatusOK {
		t.Fatal("refused label answered", res.Body)
	}
}

// TestAccMavenNoStoreMappedChecksum: on a remote whose upstream has
// another layout, the checksum file asked for is the mapped file's, as
// HttpRepo.downloadResource translates the path it downloads
// (convertRequestPathIfNeeded), and a POM's checksum is not admitted as a
// POM.
func TestAccMavenNoStoreMappedChecksum(t *testing.T) {
	w := newMavenRemoteWorld(t)
	w.c.RepoLayoutRef, w.c.RemoteRepoLayoutRef = "gradle-default", "maven-2-default"
	w.c.StoreArtifactsLocally = new(false)
	w.update()
	pom := "org/acme/app/1.0/app-1.0.pom"
	mavenSource(w, pom, []byte(sourcePOM("app", "1.0")), "application/xml")
	w.up.Set("/"+pom+".sha1", fakeupstream.Resource{Body: []byte(strings.Repeat("5", 40))})
	res := w.request("GET", "/artifactory/remote/org.acme/app/ivy-1.0.xml.sha1", "reader", "")
	remoteStatus(t, res, 200)
	if res.Body.String() != strings.Repeat("5", 40) || w.up.Count("GET", "/"+pom) != 0 ||
		w.up.Count("GET", "/"+pom+".sha1") != 1 {
		t.Fatal(res.Body, w.up.Requests())
	}
}
