package terraformhttp

import (
	"encoding/binary"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

// xzCut is data, one xz stream without stream padding, less its index and
// footer, which ulikunitz/xz's reader decoded whole.
func xzCut(t *testing.T, data []byte) []byte {
	t.Helper()
	footer := data[len(data)-12:]
	if string(footer[10:]) != "YZ" {
		t.Fatal("no xz footer at the end")
	}
	return data[:len(data)-int(binary.LittleEndian.Uint32(footer[4:8])+1)*4-12]
}

// A remote module whose xz stream lost its index and footer is refused, as
// xz -t and liblzma refuse it and as the remote's check of every
// compression trailer promises, both as a .tar.xz, whose tar reader stops
// at the archive's end and leaves the rest to the trailer check, and as a
// plain .xz (#1331). The whole files are admitted.
func TestUnitRemoteModuleXZNeedsItsWholeStream(t *testing.T) {
	for name, whole := range map[string][]byte{
		"module.tar.xz": archiveXZ(t, archiveTar(t, "main.tf")),
		"module.xz":     archiveXZ(t, []byte("module bytes")),
	} {
		completeArchive(t, whole, name, true)
		completeArchive(t, xzCut(t, whole), name, false)
	}
}

// The module download route answers a remote's cut .tar.xz or .xz module
// with the 502 it gives any invalid upstream archive, and the whole one
// with 204 and its location (#1331).
func TestAccRemoteModuleDownloadRefusesACutXZArchive(t *testing.T) {
	for _, kind := range []string{"tar.xz", "xz"} {
		t.Run(kind, func(t *testing.T) {
			whole := archiveXZ(t, []byte("module bytes"))
			if kind == "tar.xz" {
				whole = archiveXZ(t, archiveTar(t, "main.tf"))
			}
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			f.location("1.0.0", "/archives/module?archive="+kind)
			serve := func(body []byte) {
				f.up.Set("/archives/module", fakeupstream.Resource{
					Body:   body,
					Header: http.Header{"Content-Type": {"application/octet-stream"}},
				})
			}
			serve(xzCut(t, whole))
			w := f.do("GET", remoteModuleBase+"1.0.0/download", "admin")
			expect(t, w, http.StatusBadGateway)
			if strings.TrimSpace(w.Body.String()) != `{"errors":["Bad Gateway"]}` {
				t.Fatalf("cut %s module: %s", kind, w.Body)
			}
			serve(whole)
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), http.StatusNoContent)
		})
	}
}
