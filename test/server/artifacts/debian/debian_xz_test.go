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

// A package whose control.tar.xz or data.tar.xz lost its index and footer,
// which dpkg refuses, is refused at deploy with the 400 any unreadable
// package gets, and a remote's upstream package or Packages.xz cut the same
// way answers the 502 of any invalid upstream file and is not served
// (#1331). The whole files are still admitted.
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
		r := w.Request(http.MethodPut, "/artifactory/deb-local/"+pkg, "admin", string(c.body))
		want := `"message":"Invalid Debian package: debian: invalid binary package: ` + c.want + `"`
		if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), want) {
			t.Errorf(
				"deploy without the %s index answered %d %s, want 400 %s",
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
	for _, p := range []string{pkg, "pool/main/h/hello/hello_1.0-2_amd64.deb", "dists/stable/main/binary-amd64/Packages.xz"} {
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
}

// A virtual over a remote whose upstream Packages.xz lost its xz index and
// footer answers that distribution's Release and Packages with the 502 the
// remote gives the index, as it does for any invalid member index, where it
// merged the cut index before (#1331). A distribution whose index is whole
// is still merged.
func TestAccDebianVirtualRefusesAMemberIndexWithoutItsXZIndex(t *testing.T) {
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
		if r.Code != http.StatusBadGateway || strings.TrimSpace(r.Body.String()) != badGateway {
			t.Errorf(
				"virtual %s over a cut index answered %d %s, want 502 %s",
				p,
				r.Code,
				r.Body,
				badGateway,
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
