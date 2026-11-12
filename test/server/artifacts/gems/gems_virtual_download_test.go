package gems_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	gemfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// unreadableGem is a stored archive that no gem reader accepts. The upload
// routes refuse it with a 400, so storeUnreadableGem writes it past them.
const unreadableGem = "not a RubyGem archive"

func (w *world) storeUnreadableGem(key, name string) {
	w.t.Helper()
	u, err := w.blobs.BeginUpload(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := u.Append(w.t.Context(), bytes.NewReader([]byte(unreadableGem))); err != nil {
		w.t.Fatal(err)
	}
	info, err := u.Commit(w.t.Context(), blob.Digests{})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.db.Items().Put(w.t.Context(), artmodel.Item{
		RepoKey: key, Path: "gems", Name: name,
		Actual: kernel.Checksums{SHA256: info.Digests.SHA256}, Size: int64(len(unreadableGem)),
	}); err != nil {
		w.t.Fatal(err)
	}
}

// wantGem fails unless rec is a 200 whose body is want, byte for byte.
func wantGem(t *testing.T, rec *httptest.ResponseRecorder, want []byte) {
	t.Helper()
	status(t, rec, http.StatusOK)
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf(
			"got %d bytes %.40q, want %d bytes %.40q",
			rec.Body.Len(),
			rec.Body.Bytes(),
			len(want),
			want,
		)
	}
}

// A virtual gem download asks each member, in order, for the gem's own
// path, as 6.20's VirtualRepoDownloadStrategy does (decision 0032), and
// reads nothing else: what other members store, or their upstreams list,
// cannot change its answer. A member's download of a stored archive does
// not read the archive, so the virtual serves the same bytes the member's
// own route does, even ones no gem reader accepts.
func TestAccVirtualGemAsksEachMemberForItsPath(t *testing.T) {
	const gem = "gems/rake-12.3.1.gem"

	t.Run("another member stores an unreadable gem", func(t *testing.T) {
		w := newWorld(t)
		status(t, w.do(http.MethodPut, "/artifactory/gems-local/"+gem, "admin", w.gem),
			http.StatusCreated)
		w.storeUnreadableGem("gems-fed", "other-1.0.0.gem")
		wantGem(
			t,
			w.do(http.MethodGet, "/artifactory/api/gems/gems-virtual/"+gem, "admin", nil),
			w.gem,
		)
	})

	t.Run("the requested gem is unreadable", func(t *testing.T) {
		w := newWorld(t)
		w.storeUnreadableGem("gems-local", "rake-12.3.1.gem")
		for _, key := range []string{"gems-local", "gems-virtual"} {
			wantGem(t, w.do(http.MethodGet, "/artifactory/api/gems/"+key+"/"+gem, "admin", nil),
				[]byte(unreadableGem))
		}
	})

	// The remote member's upstream lists only foo, so the remote member has
	// no rake and the local member after it serves the gem. The upstream
	// serves no info file for foo either: a download that read the members'
	// indexes to find rake would fail on it.
	t.Run("a remote upstream lists another gem", func(t *testing.T) {
		up := fakeupstream.New(t, fakeupstream.Options{})
		w := newRemoteWorld(t, up)
		foo, err := gemfmt.Read(t.Context(), bytes.NewReader(w.gem), int64(len(w.gem)), gem)
		if err != nil {
			t.Fatal(err)
		}
		foo.Name = "foo"
		up.Set(
			"/versions",
			fakeupstream.Resource{Body: gemfmt.CompactVersions([]gemfmt.Package{foo})},
		)
		v, err := w.db.Repos().Get(t.Context(), "gems-remote-virtual")
		if err != nil {
			t.Fatal(err)
		}
		v.Repositories = []string{"gems-remote", "gems-local"}
		if err := w.db.Repos().Update(t.Context(), v); err != nil {
			t.Fatal(err)
		}
		status(t, w.do(http.MethodPut, "/artifactory/gems-local/"+gem, "admin", w.gem),
			http.StatusCreated)
		wantGem(
			t,
			w.do(http.MethodGet, "/artifactory/api/gems/gems-remote-virtual/"+gem, "admin", nil),
			w.gem,
		)
	})
}
