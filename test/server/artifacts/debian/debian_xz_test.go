package debian_test

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// xzOf is body in one xz stream, as xz writes it.
func xzOf(t *testing.T, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// xzCut is data, one xz stream without stream padding, less its index and
// footer: the footer's backward size plus the footer.
func xzCut(t *testing.T, data []byte) []byte {
	t.Helper()
	footer := data[len(data)-12:]
	if string(footer[10:]) != "YZ" {
		t.Fatal("no xz footer at the end")
	}
	return data[:len(data)-int(binary.LittleEndian.Uint32(footer[4:8])+1)*4-12]
}

// tarOf is a tar archive holding one file.
func tarOf(t *testing.T, name, content string) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	if err := tw.WriteHeader(
		&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// debOf is a .deb whose members are debian-binary and the control and data
// archives given, as dpkg-deb lays them out.
func debOf(t *testing.T, control, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("!<arch>\n")
	for _, m := range []struct {
		name string
		body []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.xz", control}, {"data.tar.xz", data}} {
		fmt.Fprintf(&out, "%-16s%-12d%-6d%-6d%-8o%-10d`\n", m.name, 0, 0, 0, 0o644, len(m.body))
		out.Write(m.body)
		if len(m.body)%2 != 0 {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}

// badGateway is the body of the 502 a remote answers for an invalid
// upstream file.
const badGateway = `{"errors":[{"message":"Bad Gateway","status":502}]}`

// notFound is the body of the 404 the server answers for a path it holds
// nothing at.
const notFound = `{"errors":[{"message":"Could not find resource","status":404}]}`

// A package whose control.tar.xz or data.tar.xz lost its index and footer,
// which dpkg refuses, is stored at deploy and left unindexed with the reason
// in deb.index.status, as any unreadable package is (#1355; RTFACT-16378
// says 7.x stores a package whose control.tar.xz it cannot read and does not
// index it), and a whole package that replaces it clears the reason. A
// remote's upstream package cut the same way answers the 502 of any invalid
// upstream file and is not served (#1331), while a Packages.xz cut the same
// way is served and cached as fetched, as any upstream index is (#1356;
// RTFACT-27303). The whole files are still admitted.
func TestAccDebianRefusesAnXZStreamWithoutItsIndex(t *testing.T) {
	const control = "Package: hello\nVersion: 1.0-1\nArchitecture: amd64\n" +
		"Maintainer: A <a@example.com>\nDescription: hello\n"
	controlXZ := xzOf(t, tarOf(t, "./control", control))
	dataXZ := xzOf(t, tarOf(t, "./usr/bin/hello", "hello"))
	whole := debOf(t, controlXZ, dataXZ)
	const pkg = "pool/main/h/hello/hello_1.0-1_amd64.deb"
	packages := xzOf(t, []byte(stanza(pkg, whole)))
	up := newDebianArchive(t, map[string][]byte{
		pkg: debOf(t, controlXZ, xzCut(t, dataXZ)),
		"pool/main/h/hello/hello_1.0-2_amd64.deb":    debOf(t, xzCut(t, controlXZ), dataXZ),
		"pool/main/h/hello/hello_1.0-3_amd64.deb":    whole,
		"dists/stable/main/binary-amd64/Packages.xz": xzCut(t, packages),
		"dists/other/main/binary-amd64/Packages.xz":  packages,
	})
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = repo.Debian
	w.Repo.URL = up.URL
	w.Update()
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "deb-local", Class: repo.Local, PackageType: repo.Debian,
	})); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		body []byte
		want string
	}{
		"data.tar.xz":    {debOf(t, controlXZ, xzCut(t, dataXZ)), "data tar exceeds limit"},
		"control.tar.xz": {debOf(t, xzCut(t, controlXZ), dataXZ), "missing or excessive control data"},
	} {
		harness.WantStatus(
			t,
			w.Request(http.MethodPut, "/artifactory/deb-local/"+pkg, "admin", string(c.body)),
			http.StatusCreated,
		)
		r := w.Request(
			http.MethodGet,
			"/artifactory/api/storage/deb-local/"+pkg+"?properties",
			"admin",
			"",
		)
		want := `{"properties":{"deb.index.status":["debian: invalid binary package: ` + c.want +
			`"]},"uri":"http://example.com/artifactory/api/storage/deb-local/` + pkg + `"}`
		if r.Code != http.StatusOK || strings.TrimSpace(r.Body.String()) != want {
			t.Errorf(
				"package without the %s index has properties %d %s, want 200 %s",
				name,
				r.Code,
				r.Body,
				want,
			)
		}
	}
	harness.WantStatus(
		t,
		w.Request(http.MethodPut, "/artifactory/deb-local/"+pkg, "admin", string(whole)),
		http.StatusCreated,
	)
	r := w.Request(
		http.MethodGet,
		"/artifactory/api/storage/deb-local/"+pkg+"?properties",
		"admin",
		"",
	)
	if r.Code != http.StatusOK || strings.Contains(r.Body.String(), debian.PropIndexStatus) {
		t.Errorf("whole package has properties %d %s, want no %s", r.Code, r.Body,
			debian.PropIndexStatus)
	}
	for _, p := range []string{pkg, "pool/main/h/hello/hello_1.0-2_amd64.deb"} {
		r := w.Request(http.MethodGet, "/artifactory/remote/"+p, "admin", "")
		if r.Code != http.StatusBadGateway || strings.TrimSpace(r.Body.String()) != badGateway {
			t.Errorf("upstream %s without its index answered %d with %d bytes, want 502 %s",
				p, r.Code, r.Body.Len(), badGateway)
		}
	}
	for _, p := range []string{"pool/main/h/hello/hello_1.0-3_amd64.deb", "dists/other/main/binary-amd64/Packages.xz"} {
		harness.WantStatus(
			t,
			w.Request(http.MethodGet, "/artifactory/remote/"+p, "admin", ""),
			http.StatusOK,
		)
	}
	for _, repoKey := range []string{"remote", "remote-cache"} {
		r = w.Request(http.MethodGet,
			"/artifactory/"+repoKey+"/dists/stable/main/binary-amd64/Packages.xz", "admin", "")
		if r.Code != http.StatusOK || !bytes.Equal(r.Body.Bytes(), xzCut(t, packages)) {
			t.Errorf("%s Packages.xz without its xz index answered %d with %d bytes, "+
				"want 200 with the %d bytes fetched", repoKey, r.Code, r.Body.Len(),
				len(xzCut(t, packages)))
		}
	}
}

// A virtual over a remote whose upstream Packages.xz lost its xz index and
// footer leaves that index out of what it merges, where it merged the cut
// index before #1331 and answered 502 after it: the remote caches the index
// as fetched (#1356), and a member index the virtual cannot decode is left
// out as a missing one is [I]. With no other index for the distribution,
// its Release and Packages are not found, as for a distribution no member
// holds. A distribution whose index is whole is still merged.
func TestAccDebianVirtualLeavesOutAMemberIndexWithoutItsXZIndex(t *testing.T) {
	packages := xzOf(t, []byte(stanza("pool/main/a/alpha/alpha_1.0_amd64.deb", []byte("x"))))
	files := map[string][]byte{}
	for dist, body := range map[string][]byte{"stable": xzCut(t, packages), "other": packages} {
		release, err := debian.Release(dist, []string{"main"}, []string{"amd64"},
			[]debian.ReleaseEntry{{Path: "main/binary-amd64/Packages.xz", Body: body}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		files["dists/"+dist+"/Release"] = release
		files["dists/"+dist+"/main/binary-amd64/Packages.xz"] = body
	}
	up := newDebianArchive(t, files)
	w := harness.NewRemoteWorld(t)
	w.Repo.PackageType = repo.Debian
	w.Repo.URL = up.URL
	w.Update()
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "deb-virtual", Class: repo.Virtual, PackageType: repo.Debian,
		Repositories: []string{"remote"},
	})); err != nil {
		t.Fatal(err)
	}
	for p, whole := range map[string]string{
		"Release":                    "Suite: other",
		"main/binary-amd64/Packages": "Package: alpha",
	} {
		r := w.Request(http.MethodGet, "/artifactory/deb-virtual/dists/stable/"+p, "admin", "")
		if r.Code != http.StatusNotFound || strings.TrimSpace(r.Body.String()) != notFound {
			t.Errorf(
				"virtual %s over a cut index answered %d %s, want 404 %s",
				p,
				r.Code,
				r.Body,
				notFound,
			)
		}
		r = w.Request(http.MethodGet, "/artifactory/deb-virtual/dists/other/"+p, "admin", "")
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), whole) {
			t.Errorf(
				"virtual %s over a whole index answered %d %s, want 200 with %q",
				p,
				r.Code,
				r.Body,
				whole,
			)
		}
	}
}
