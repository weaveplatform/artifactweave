package cran_test

import (
	"archive/tar"
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// xzSource is the .tar.xz source package of name 1.0, as R CMD build
// --compression=xz writes it, and the same file less its index and footer:
// the footer's backward size plus the footer.
func xzSource(t *testing.T, name string) (whole, cut []byte) {
	t.Helper()
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	description := "Package: " + name + "\nVersion: 1.0\nDescription: xz source\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: name + "/DESCRIPTION", Mode: 0o644, Size: int64(len(description)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(description)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	whole = out.Bytes()
	footer := whole[len(whole)-12:]
	return whole, whole[:len(whole)-int(binary.LittleEndian.Uint32(footer[4:8])+1)*4-12]
}

// A .tar.xz source package whose stream lost its index and footer, which R
// refuses through liblzma, is refused at deploy with the 400 any unreadable
// package gets, and a remote's upstream package cut the same way answers
// the 502 of any invalid upstream package, through the remote and through
// a virtual over it (#1331). The whole files are still admitted.
func TestAccCRANRefusesAnXZStreamWithoutItsIndex(t *testing.T) {
	demo, demoCut := xzSource(t, "demo")
	other, _ := xzSource(t, "other")
	index := ""
	for name, body := range map[string][]byte{"demo": demoCut, "other": other} {
		index += fmt.Sprintf("Package: %s\nVersion: 1.0\nFile: %s_1.0.tar.xz\nMD5sum: %x\n\n",
			name, name, md5.Sum(body))
	}
	files := map[string][]byte{
		"src/contrib/PACKAGES":         []byte(index),
		"src/contrib/demo_1.0.tar.xz":  demoCut,
		"src/contrib/other_1.0.tar.xz": other,
	}
	r := chi.NewRouter()
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[chi.URLParam(r, "*")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})
	up := httptest.NewServer(r)
	t.Cleanup(up.Close)
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = repo.Cran
	w.Repo.URL = up.URL
	w.Update()
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "cran-local", Class: repo.Local, PackageType: repo.Cran,
	})); err != nil {
		t.Fatal(err)
	}
	const want = `"message":"Invalid CRAN package: cran: invalid cran package"`
	for _, p := range []string{"src/contrib/demo_1.0.tar.xz", "src/contrib/Archive/demo/demo_1.0.tar.xz"} {
		res := w.Request(http.MethodPut, "/artifactory/cran-local/"+p, "admin", string(demoCut))
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), want) {
			t.Errorf(
				"deploy of %s without its index answered %d %s, want 400 %s",
				p,
				res.Code,
				res.Body,
				want,
			)
		}
		harness.WantStatus(
			t,
			w.Request(http.MethodPut, "/artifactory/cran-local/"+p, "admin", string(demo)),
			http.StatusCreated,
		)
	}
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "cran-virtual", Class: repo.Virtual, PackageType: repo.Cran,
		Repositories: []string{"remote"},
	})); err != nil {
		t.Fatal(err)
	}
	const badGateway = `{"errors":[{"message":"Bad Gateway","status":502}]}`
	// The virtual serves the remote's package through it, so it answers the
	// remote's 502.
	for _, base := range []string{"/artifactory/remote/", "/artifactory/cran-virtual/"} {
		res := w.Request(http.MethodGet, base+"src/contrib/demo_1.0.tar.xz", "admin", "")
		if res.Code != http.StatusBadGateway || strings.TrimSpace(res.Body.String()) != badGateway {
			t.Errorf(
				"%s: upstream package without its index answered %d with %d bytes, want 502 %s",
				base,
				res.Code,
				res.Body.Len(),
				badGateway,
			)
		}
		harness.WantStatus(
			t,
			w.Request(http.MethodGet, base+"src/contrib/other_1.0.tar.xz", "admin", ""),
			http.StatusOK,
		)
	}
}
