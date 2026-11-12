package debian_test

import (
	"crypto/md5" //nolint:gosec // Debian Packages wire checksum.
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// debianArchive is an upstream Debian archive that serves fixed files by
// path and counts the GETs of each.
type debianArchive struct {
	*httptest.Server
	files map[string][]byte
	mu    sync.Mutex
	asked map[string]int
}

func newDebianArchive(t *testing.T, files map[string][]byte) *debianArchive {
	t.Helper()
	a := &debianArchive{files: files, asked: map[string]int{}}
	r := chi.NewRouter()
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		p := chi.URLParam(r, "*")
		a.mu.Lock()
		a.asked[p]++
		a.mu.Unlock()
		body, ok := a.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	a.Server = httptest.NewServer(r)
	t.Cleanup(a.Close)
	return a
}

func (a *debianArchive) gets(p string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asked[p]
}

// debianForm is index in the form suffix names, as a repository writes it.
func debianForm(t *testing.T, index []byte, suffix string) []byte {
	t.Helper()
	forms, err := debian.IndexForms(index, []string{"lzma"})
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range forms {
		if form.Suffix == suffix {
			return form.Body
		}
	}
	t.Fatalf("no %q form", suffix)
	return nil
}

// stanza is a Packages stanza for the alpha package's file body at file.
func stanza(file string, body []byte) string {
	return fmt.Sprintf("Package: alpha\nVersion: 1.0\nArchitecture: amd64\nFilename: %s\n"+
		"Size: %d\nMD5sum: %x\nSHA256: %x\n\n", file, len(body),
		md5.Sum(body), sha256.Sum256(body)) //nolint:gosec // Packages wire checksum.
}

// TestAccDebianRemoteRelaysWhatAptReadsThroughItsCache: a remote Debian
// repository over a real upstream, through the remote cache and its
// validation, asks the upstream for each path of #1273's table, which it
// answered 404 before without asking, and serves the upstream's bytes: an
// installer's Packages index, a translation's and a Contents index's
// by-hash copies, a .lzma index and a package under a folder named
// by-hash. A by-hash file whose bytes do not match its name answers 502
// and is not cached, so the next GET asks the upstream again.
func TestAccDebianRemoteRelaysWhatAptReadsThroughItsCache(t *testing.T) {
	deb := repopath.Read(t, "internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb")
	const pkg = "pool/main/a/alpha/alpha_1.0_amd64.deb"
	translation := []byte("Package: alpha\nDescription-md5: 0\nDescription-en: alpha\n\n")
	contents := []byte("usr/bin/alpha\tutils/alpha\n")
	byHash := func(dir string, body []byte) string {
		return fmt.Sprintf("dists/stable/main/%sby-hash/SHA256/%x", dir, sha256.Sum256(body))
	}
	files := map[string][]byte{
		"dists/stable/main/debian-installer/binary-amd64/Packages.gz": debianForm(t,
			[]byte(stanza("pool/main/a/alpha/alpha_1.0_amd64.udeb", deb)), ".gz"),
		byHash("i18n/", translation):                   translation,
		byHash("", contents):                           contents,
		"dists/stable/main/binary-amd64/Packages.lzma": debianForm(t, []byte(stanza(pkg, deb)), ".lzma"),
		"pool/main/by-hash/SHA256/alpha_1.0_amd64.deb": deb,
		"dists/stable/main/i18n/Translation-en.bz2":    []byte("translation file"),
	}
	mismatch := byHash("i18n/", []byte("other bytes"))
	all := map[string][]byte{mismatch: translation}
	for p, body := range files {
		all[p] = body
	}
	up := newDebianArchive(t, all)
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = repo.Debian
	w.Repo.URL = up.URL
	w.Update()
	for p, body := range files {
		t.Run(p, func(t *testing.T) {
			r := w.Request(http.MethodGet, "/artifactory/remote/"+p, "admin", "")
			if r.Code != http.StatusOK || r.Body.String() != string(body) {
				t.Errorf("answered %d %q, want 200 and the upstream's bytes", r.Code, r.Body)
			}
			if up.gets(p) == 0 {
				t.Error("the upstream was not asked")
			}
		})
	}
	for range 2 {
		r := w.Request(http.MethodGet, "/artifactory/remote/"+mismatch, "admin", "")
		if r.Code != http.StatusBadGateway {
			t.Errorf("mismatched by-hash file answered %d %s, want 502", r.Code, r.Body)
		}
	}
	if n := up.gets(mismatch); n != 2 {
		t.Errorf("the upstream was asked %d times for the mismatched file, want 2", n)
	}
	_, err := w.Runtime.Store.Items().Get(t.Context(), "remote", true, mismatch)
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("mismatched by-hash file cached: %v", err)
	}
}
