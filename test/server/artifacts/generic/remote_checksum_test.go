package generic_test

// This file holds the cold checksum suites of #1495: a checksum request on
// a remote of any type is answered from the file it names, as 7.161.26's
// DownloadServiceImpl.respond hands it to respondForChecksumRequest, whose
// RemoteRepoBase.getChecksum reads the file's info through the remote's
// checksum policy, then, for a generic remote with
// retrieveSha256FromServer, the upstream's checksum file
// (getChecksumByExternalSha256).

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// declared are the X-Checksum-* values an upstream declares for a file in
// these tests. None is the file's own digest, so an answer that carries
// one came from the declaration, not from the bytes.
var declared = map[string]struct{ header, value string }{
	"md5":    {"X-Checksum-Md5", strings.Repeat("a", 32)},
	"sha1":   {"X-Checksum-Sha1", strings.Repeat("b", 40)},
	"sha256": {"X-Checksum-Sha256", strings.Repeat("c", 64)},
	"sha512": {"X-Checksum-Sha512", strings.Repeat("d", 128)},
}

// checksumNotFound is the body 7.x's respondForChecksumRequest answers
// when it finds no checksum for file.
func checksumNotFound(file string) string {
	return `{"errors":[{"message":"Checksum not found for ` + file + `","status":404}]}` + "\n"
}

// remoteOfType replaces w's remote with one of type pt and the settings
// tune makes, over the same upstream.
func remoteOfType(t *testing.T, w *remoteWorld, pt repo.PackageType, tune func(*repo.Config)) {
	t.Helper()
	c := *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: pt, URL: w.up.URL,
	})
	if tune != nil {
		tune(&c)
	}
	if err := w.rt.Store.Repos().Delete(t.Context(), "remote"); err != nil {
		t.Fatal(err)
	}
	if err := w.rt.Store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	w.c = c
}

// declaring is an upstream file that declares every checksum in declared,
// or none.
func declaring(body []byte, declares bool) fakeupstream.Resource {
	header := http.Header{"Content-Type": {"application/octet-stream"}}
	if declares {
		for _, d := range declared {
			header.Set(d.header, d.value)
		}
	}
	return fakeupstream.Resource{Body: body, Header: header}
}

// wantKeptNothing fails t when the upstream was sent a GET, or the cache
// holds any of paths.
func wantKeptNothing(t *testing.T, w *remoteWorld, paths ...string) {
	t.Helper()
	if n := w.up.Count("GET", ""); n != 0 {
		t.Fatal("checksum request downloaded", w.up.Requests())
	}
	for _, p := range paths {
		if _, err := w.rt.Store.Items().
			Get(t.Context(), "remote", true, p); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("checksum request kept", p, err)
		}
	}
}

// coldChecksumTypes are the remote types whose item route reads a file from
// the upstream, each with a file path its route admits.
var coldChecksumTypes = []struct {
	pt   repo.PackageType
	path func(n int) string
}{
	{repo.Generic, func(n int) string { return fmt.Sprintf("pkg/a%d.bin", n) }},
	{repo.Debian, func(n int) string { return fmt.Sprintf("pool/main/a/a/a_%d.0_amd64.deb", n) }},
	{repo.RPM, func(n int) string { return fmt.Sprintf("Packages/a-%d.0-1.x86_64.rpm", n) }},
	{repo.Alpine, func(n int) string { return fmt.Sprintf("v3.19/main/x86_64/a-%d.0-r0.apk", n) }},
	{repo.P2, func(n int) string { return fmt.Sprintf("plugins/a_%d.0.0.jar", n) }},
	{repo.Cran, func(n int) string { return fmt.Sprintf("src/contrib/a_%d.0.tar.gz", n) }},
	{repo.Opkg, func(n int) string { return fmt.Sprintf("feed/a_%d.0_all.ipk", n) }},
}

// TestAccRemoteColdChecksumAnswersFromFileInfo (#1495): a checksum request
// for a file a remote that stores artifacts locally does not hold is
// answered from the file's info, the upstream's answer to one HEAD, through
// the remote's checksum policy, on a remote of every type whose item route
// reads a file from the upstream, and downloads and keeps nothing, HEAD or
// GET. 7.161.26's RemoteRepoBase.getChecksum reads the info's checksums,
// which hold only the upstream's declarations as originals, through the
// policy: pass-thru answers the original, the other three the actual value
// a file never downloaded lacks, so they answer 404 "Checksum not found".
func TestAccRemoteColdChecksumAnswersFromFileInfo(t *testing.T) {
	policies := []repo.RemoteChecksumPolicy{
		repo.GenerateIfAbsent, repo.Fail, repo.IgnoreAndGenerate, repo.PassThru,
	}
	for _, tc := range coldChecksumTypes {
		for _, policy := range policies {
			t.Run(fmt.Sprintf("%s/%s", tc.pt, policy), func(t *testing.T) {
				w := newRemoteWorld(t)
				remoteOfType(t, w, tc.pt, func(c *repo.Config) {
					c.RemoteRepoChecksumPolicyType = policy
				})
				files := map[bool]string{false: tc.path(1), true: tc.path(2)}
				for declares, p := range files {
					w.up.Set("/"+p, declaring([]byte("file bytes"), declares))
				}
				requests := 0
				for declares, p := range files {
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						for alg, d := range declared {
							requests++
							res := w.request(method, "/artifactory/remote/"+p+"."+alg, "reader", "")
							if policy != repo.PassThru || !declares {
								remoteStatus(t, res, http.StatusNotFound)
								if method == http.MethodGet &&
									res.Body.String() != checksumNotFound(p) {
									t.Fatal(method, alg, res.Body)
								}
								continue
							}
							remoteStatus(t, res, http.StatusOK)
							if res.Header().Get("Content-Length") != strconv.Itoa(len(d.value)) ||
								method == http.MethodGet && res.Body.String() != d.value ||
								method == http.MethodHead && res.Body.Len() != 0 {
								t.Fatal(method, alg, res.Header(), res.Body)
							}
						}
					}
				}
				if n := w.up.Count(http.MethodHead, ""); n != requests {
					t.Fatal("want one upstream HEAD a request", n, w.up.Requests())
				}
				wantKeptNothing(t, w, files[false], files[true])
			})
		}
	}
}

// TestAccRemoteNoStoreChecksumHasNoValue (#1495): a remote outside the
// Maven family that stores nothing has no checksum for a file the upstream
// holds, whatever it declares and whatever the policy: 7.161.26's
// getChecksum reads no cache there, and getRemoteChecksum reads the
// upstream's checksum file for the Maven family alone. The answer is 404
// "Checksum not found" after one HEAD of the file; a file the upstream does
// not hold answers as that file does.
func TestAccRemoteNoStoreChecksumHasNoValue(t *testing.T) {
	for _, tc := range coldChecksumTypes[:2] {
		t.Run(string(tc.pt), func(t *testing.T) {
			w := newRemoteWorld(t)
			remoteOfType(t, w, tc.pt, func(c *repo.Config) {
				c.RemoteRepoChecksumPolicyType = repo.PassThru
				c.StoreArtifactsLocally = new(false)
			})
			p := tc.path(1)
			w.up.Set("/"+p, declaring([]byte("file bytes"), true))
			w.up.Set("/"+p+".sha1", fakeupstream.Resource{Body: []byte(strings.Repeat("e", 40))})
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				res := w.request(method, "/artifactory/remote/"+p+".sha1", "reader", "")
				remoteStatus(t, res, http.StatusNotFound)
				if method == http.MethodGet && res.Body.String() != checksumNotFound(p) {
					t.Fatal(res.Body)
				}
			}
			if w.up.Count(http.MethodHead, "/"+p) != 2 || w.up.Count("", "/"+p+".sha1") != 0 {
				t.Fatal(w.up.Requests())
			}
			missing := tc.path(9)
			gone := w.request(http.MethodGet, "/artifactory/remote/"+missing+".sha1", "reader", "")
			file := w.request(http.MethodGet, "/artifactory/remote/"+missing, "reader", "")
			if gone.Code != file.Code || gone.Body.String() != file.Body.String() {
				t.Fatal(gone.Code, gone.Body, file.Code, file.Body)
			}
			wantKeptNothing(t, w)
		})
	}
}

// TestAccGenericRemoteChecksumFallbackOrder (#1495): a generic remote with
// retrieveSha256FromServer reads the upstream's checksum file only once
// the upstream found the file and the file's info yielded no value, as
// 7.161.26's getChecksum calls getChecksumByExternalSha256 after the
// policy, for every algorithm and under every policy, fail included, and
// whether or not the remote stores artifacts locally.
func TestAccGenericRemoteChecksumFallbackOrder(t *testing.T) {
	sums := map[string]string{
		"md5":    strings.Repeat("1", 32),
		"sha1":   strings.Repeat("2", 40),
		"sha256": strings.Repeat("3", 64),
		"sha512": strings.Repeat("4", 128),
	}
	for _, policy := range []repo.RemoteChecksumPolicy{
		repo.GenerateIfAbsent, repo.Fail, repo.IgnoreAndGenerate, repo.PassThru,
	} {
		for _, store := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/store=%v", policy, store), func(t *testing.T) {
				w := newRemoteWorld(t)
				w.c.RemoteRepoChecksumPolicyType = policy
				w.c.RetrieveSha256FromServer = new(true)
				w.c.StoreArtifactsLocally = new(store)
				w.update()
				for _, declares := range []bool{false, true} {
					p := fmt.Sprintf("pkg/declared-%v.bin", declares)
					w.up.Set("/"+p, declaring([]byte("file bytes"), declares))
					for alg, sum := range sums {
						w.up.Set("/"+p+"."+alg, fakeupstream.Resource{
							Body:   []byte(sum + "  file.bin\n"),
							Header: http.Header{"Content-Type": {"text/plain"}},
						})
					}
					for alg, sum := range sums {
						want := sum
						if store && policy == repo.PassThru && declares {
							want = declared[alg].value
						}
						for _, method := range []string{http.MethodGet, http.MethodHead} {
							res := w.request(method, "/artifactory/remote/"+p+"."+alg, "reader", "")
							remoteStatus(t, res, http.StatusOK)
							if res.Header().Get("Content-Length") != strconv.Itoa(len(want)) ||
								method == http.MethodGet && res.Body.String() != want {
								t.Fatal(p, method, alg, res.Header(), res.Body)
							}
						}
						fetched := w.up.Count(http.MethodGet, "/"+p+"."+alg)
						if want == declared[alg].value && fetched != 0 ||
							want == sum && fetched != 2 {
							t.Fatal("checksum file asked for out of order", p, alg, w.up.Requests())
						}
					}
					if w.up.Count(http.MethodGet, "/"+p) != 0 {
						t.Fatal("checksum request asked for the file's body", w.up.Requests())
					}
				}
			})
		}
	}
}

// TestAccGenericRemoteChecksumNeedsTheFile (#1495): with
// retrieveSha256FromServer, a checksum request for a file the upstream does
// not hold answers as that file does, and the checksum file is never asked
// for: 7.161.26's respond answers a resource it did not find before its
// checksum branch. An upstream without the checksum file answers 404
// "Checksum not found", as handleRemoteRequestException turns the
// upstream's 404 into no value; a refused label on the checksum file is
// refused, as HttpRepo.downloadResource's blockMismatchingMimeTypes refuses
// it.
func TestAccGenericRemoteChecksumNeedsTheFile(t *testing.T) {
	w := newRemoteWorld(t)
	w.c.RetrieveSha256FromServer = new(true)
	w.update()
	w.up.Set("/gone.bin.sha256", fakeupstream.Resource{Body: []byte(strings.Repeat("3", 64))})
	gone := w.request(http.MethodGet, "/artifactory/remote/gone.bin.sha256", "reader", "")
	file := w.request(http.MethodGet, "/artifactory/remote/gone.bin", "reader", "")
	if gone.Code != http.StatusNotFound || gone.Body.String() != file.Body.String() ||
		w.up.Count("", "/gone.bin.sha256") != 0 {
		t.Fatal(gone.Code, gone.Body, file.Body, w.up.Requests())
	}

	w.up.Set("/bare.bin", fakeupstream.Resource{Body: []byte("bare bytes")})
	bare := w.request(http.MethodGet, "/artifactory/remote/bare.bin.sha1", "reader", "")
	remoteStatus(t, bare, http.StatusNotFound)
	if bare.Body.String() != checksumNotFound("bare.bin") {
		t.Fatal(bare.Body)
	}

	w.up.Set("/login.bin", fakeupstream.Resource{Body: []byte("login bytes")})
	w.up.Set("/login.bin.sha256", fakeupstream.Resource{
		Body:   []byte("<html>login</html>"),
		Header: http.Header{"Content-Type": {"text/html"}},
	})
	refused := w.request(http.MethodGet, "/artifactory/remote/login.bin.sha256", "reader", "")
	remoteStatus(t, refused, http.StatusConflict)
	for _, p := range []string{"bare.bin", "login.bin", "login.bin.sha256"} {
		if _, err := w.rt.Store.Items().
			Get(t.Context(), "remote", true, p); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("checksum request kept", p, err)
		}
	}
	if w.up.Count(http.MethodGet, "/bare.bin") != 0 ||
		w.up.Count(http.MethodGet, "/login.bin") != 0 {
		t.Fatal("checksum request asked for the file's body", w.up.Requests())
	}
}

// TestAccRemoteWarmChecksumUnchanged (#1495): a checksum request for a file
// the cache holds answers from the kept copy and asks the upstream nothing,
// on a generic remote and on an opkg remote, whose packages only its own
// route fetches.
func TestAccRemoteWarmChecksumUnchanged(t *testing.T) {
	ipk := repopath.Read(t, "internal/artifacts/ecosystem/opkg/testdata/464xlat_13_x86_64.ipk")
	for _, tc := range []struct {
		pt   repo.PackageType
		path string
		body []byte
	}{
		{repo.Generic, "pkg/warm.bin", []byte("warm bytes")},
		{repo.Opkg, "feed/464xlat_13_x86_64.ipk", ipk},
	} {
		t.Run(string(tc.pt), func(t *testing.T) {
			w := newRemoteWorld(t)
			remoteOfType(t, w, tc.pt, func(c *repo.Config) {
				c.RemoteRepoChecksumPolicyType = repo.PassThru
			})
			w.up.Set("/"+tc.path, declaring(tc.body, false))
			if tc.pt == repo.Opkg {
				// The opkg route fetches a package its feed's index lists,
				// so the upstream serves the index a local feed writes.
				w.up.Set(
					"/feed/Packages",
					fakeupstream.Resource{Body: opkgFeedIndex(t, w, tc.body)},
				)
			}
			remoteStatus(t, w.request(http.MethodGet, "/artifactory/remote/"+tc.path, "reader", ""),
				http.StatusOK)
			it, err := w.rt.Store.Items().Get(t.Context(), "remote", true, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			before := len(w.up.Requests())
			res := w.request(http.MethodGet, "/artifactory/remote/"+tc.path+".sha1", "reader", "")
			remoteStatus(t, res, http.StatusOK)
			if res.Body.String() != it.Actual.SHA1 || len(w.up.Requests()) != before {
				t.Fatal(res.Body, it.Actual.SHA1, w.up.Requests()[before:])
			}
		})
	}
}

// opkgFeedIndex returns the Packages index a local opkg feed holding ipk
// at feed/464xlat_13_x86_64.ipk writes, in w's runtime.
func opkgFeedIndex(t *testing.T, w *remoteWorld, ipk []byte) []byte {
	t.Helper()
	local := *repo.Defaults(&repo.Config{Key: "opkg-local", Class: repo.Local, PackageType: repo.Opkg})
	if err := w.rt.Store.Repos().Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	remoteStatus(t, w.request(http.MethodPut, "/artifactory/opkg-local/feed/464xlat_13_x86_64.ipk",
		"admin", string(ipk)), http.StatusCreated)
	items := &item.Service{Store: w.rt.Store, Blobs: w.rt.Blobs, Formats: ecosystem.For}
	if err := (opkg.Handler{}).Work(t.Context(), items.Env(),
		work.WorkItem{Kind: opkg.KindIndex, Key: "opkg-local"}); err != nil {
		t.Fatal(err)
	}
	index := w.request(http.MethodGet, "/artifactory/opkg-local/feed/Packages", "admin", "")
	remoteStatus(t, index, http.StatusOK)
	return index.Body.Bytes()
}

// TestAccP2RemoteChecksumOfAClassedFile (#1495): a P2 remote answers the
// checksum request of a plugin it does not hold from the plugin's info,
// and of one it holds from the kept copy, where its route answered 404 for
// both: 7.161.26 answers a checksum request on a remote of any type through
// respondForChecksumRequest. A path P2 does not class, checksum or not, is
// still not found.
func TestAccP2RemoteChecksumOfAClassedFile(t *testing.T) {
	w := newRemoteWorld(t)
	remoteOfType(t, w, repo.P2, func(c *repo.Config) {
		c.RemoteRepoChecksumPolicyType = repo.PassThru
	})
	var jar bytes.Buffer
	zw := zip.NewWriter(&jar)
	if _, err := zw.Create("META-INF/MANIFEST.MF"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	const plugin = "plugins/a_1.0.0.jar"
	w.up.Set("/"+plugin, declaring(jar.Bytes(), true))
	cold := w.request(http.MethodGet, "/artifactory/remote/"+plugin+".sha1", "reader", "")
	remoteStatus(t, cold, http.StatusOK)
	if cold.Body.String() != declared["sha1"].value {
		t.Fatal(cold.Body)
	}
	remoteStatus(t, w.request(http.MethodGet, "/artifactory/remote/"+plugin, "reader", ""),
		http.StatusOK)
	it, err := w.rt.Store.Items().Get(t.Context(), "remote", true, plugin)
	if err != nil {
		t.Fatal(err)
	}
	warm := w.request(http.MethodGet, "/artifactory/remote/"+plugin+".md5", "reader", "")
	remoteStatus(t, warm, http.StatusOK)
	if want := it.Original.MD5; warm.Body.String() != want {
		t.Fatal(warm.Body, want)
	}
	remoteStatus(t, w.request(http.MethodGet, "/artifactory/remote/other/a.txt.sha1", "reader", ""),
		http.StatusNotFound)
}
