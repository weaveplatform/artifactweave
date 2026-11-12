package p2_test

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

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

// badGateway is the body of the 502 a remote answers for an invalid
// upstream file.
const badGateway = `{"errors":[{"message":"Bad Gateway","status":502}]}`

// A remote's upstream content.xml.xz or artifacts.xml.xz whose stream lost
// its index and footer, which Eclipse's XZInputStream refuses, answers the
// 502 of any invalid upstream metadata and is not served, through the
// remote and through a virtual over it (#1331). The whole files are still
// served.
func TestAccP2RemoteRefusesAnXZStreamWithoutItsIndex(t *testing.T) {
	files := map[string][]byte{}
	for _, name := range []string{"content.xml.xz", "artifacts.xml.xz"} {
		body := p2Fixture(t, name)
		files["whole/"+name] = body
		files["cut/"+name] = xzCut(t, body)
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
	w.Repo.PackageType = repo.P2
	w.Repo.URL = up.URL
	w.Update()
	if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2,
		Repositories: []string{"remote"},
	})); err != nil {
		t.Fatal(err)
	}
	// The virtual serves a member's file under the member's key, through
	// the remote, so it answers the remote's 502.
	for _, base := range []string{"/artifactory/remote/", "/artifactory/p2-virtual/remote/"} {
		for _, name := range []string{"content.xml.xz", "artifacts.xml.xz"} {
			res := w.Request(http.MethodGet, base+"cut/"+name, "admin", "")
			if res.Code != http.StatusBadGateway ||
				strings.TrimSpace(res.Body.String()) != badGateway {
				t.Errorf("%s: upstream %s without its index answered %d with %d bytes, want 502 %s",
					base, name, res.Code, res.Body.Len(), badGateway)
			}
			harness.WantStatus(
				t,
				w.Request(http.MethodGet, base+"whole/"+name, "admin", ""),
				http.StatusOK,
			)
		}
	}
}
