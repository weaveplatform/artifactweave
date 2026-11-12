package nixhttp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// files lists the paths of the files stored in repository key.
func (f *fixture) files(key string) []string {
	f.t.Helper()
	items, _, err := f.db.Items().
		Descendants(f.t.Context(), key, false, "", kernel.Page{Limit: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	var paths []string
	for _, it := range items {
		if !it.Folder {
			paths = append(paths, it.FullPath())
		}
	}
	slices.Sort(paths)
	return paths
}

func (f *fixture) has(key, path string) bool {
	f.t.Helper()
	_, err := f.db.Items().Get(f.t.Context(), key, false, path)
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		f.t.Fatal(err)
	}
	return err == nil
}

// Publishing a store path the way nix copy --to does, the NAR to
// nar/<file> and then the narinfo to <hash>.narinfo, stores both in the
// store path's folder under binary-cache, as 7.x stores them and jf nix
// queries them, and nothing at the paths the client uploaded to (#1296).
func TestAccNixPublishStoresBothFilesInTheStorePathsFolder(t *testing.T) {
	f := newFixture(t)
	status(t, f.do("PUT", prefix+f.info.URL, f.data, ""), 201)
	if got := f.files("local"); !slices.Equal(got, []string{f.info.URL}) {
		t.Fatalf("held upload: %v", got)
	}
	status(t, f.do("PUT", prefix+infoPath, f.meta, ""), 201)
	want := []string{storedInfo, f.storedNAR()}
	slices.Sort(want)
	if got := f.files("local"); !slices.Equal(got, want) {
		t.Fatalf("stored %v, want %v", got, want)
	}
	if f.has("local", "nar") || f.has("local", infoPath) {
		t.Fatal("the client's upload paths kept")
	}
	p, err := f.db.Packages().Find(t.Context(), "local", "weave-fixture")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := f.db.Packages().Versions(t.Context(), p.ID)
	if err != nil || len(versions) != 1 || versions[0].Version != infoPath[:32] {
		t.Fatalf("versions %v %v", versions, err)
	}
	// A narinfo that does not parse is still refused as one, and one whose
	// NAR was never uploaded still needs it.
	wantBody(t, f.do("PUT", prefix+infoPath, []byte("bad"), ""), 400, "invalid Nix narinfo")
	if err = f.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "empty", Class: repo.Local, PackageType: repo.Nix,
	})); err != nil {
		t.Fatal(err)
	}
	wantBody(t, f.do("PUT", "/artifactory/api/nix/empty/"+infoPath, f.meta, ""), 409,
		"Nix narinfo requires its matching archive")
	if got := f.files("empty"); len(got) != 0 {
		t.Fatalf("refused publication stored %v", got)
	}
}

// A store path whose NAR is byte for byte another's, which Nix uploads
// once, is published from the NAR stored for the other store path, so its
// narinfo is not refused when the upload it would have used was released.
func TestAccNixPublishReusesAnIdenticalStoredNAR(t *testing.T) {
	f := newFixture(t)
	f.publish()
	other := f.info
	hash := strings.Repeat("0", 32)
	other.StorePath = "/nix/store/" + hash + "-weave-fixture"
	other.Signatures = nil
	status(t, f.do("PUT", prefix+hash+".narinfo", other.Encode(other.URL), ""), 201)
	stored := nixfmt.ArchiveItem(hash, other.URL, false)
	if !f.has("local", stored) || !f.has("local", f.storedNAR()) {
		t.Fatalf("stored %v", f.files("local"))
	}
	w := f.do("GET", prefix+hash+".narinfo", nil, "")
	status(t, w, 200)
	url, want := downloadURL(w.Body.Bytes()), "nar/"+hash+"/"+strings.TrimPrefix(other.URL, "nar/")
	if url != want {
		t.Fatalf("download URL %q, want %q", url, want)
	}
	f.wantArchive(prefix + url)
}

func (f *fixture) grant(name string, actions api.Actions, include ...string) {
	f.t.Helper()
	if err := f.db.Permissions().Create(f.t.Context(), accesstest.Permission{
		Name: name,
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"publisher": actions}},
				Targets: map[string]accesstest.Target{"local": {Include: include}},
			},
		},
	}); err != nil {
		f.t.Fatal(err)
	}
}

// Publishing a narinfo stores its NAR in the store path's folder for the
// caller, who needs Write there as on any upload, and Write alone: as
// before, a publisher who may not read what it uploads still publishes.
func TestAccNixPublishNeedsWriteOnTheFolder(t *testing.T) {
	t.Run("write only", func(t *testing.T) {
		f := newFixture(t)
		f.grant("write", api.ActionWrite, "nar/**", "binary-cache/**")
		status(t, f.do("PUT", prefix+f.info.URL, f.data, "publisher"), 201)
		status(t, f.do("PUT", prefix+infoPath, f.meta, "publisher"), 201)
		if got := f.files("local"); len(got) != 2 {
			t.Fatalf("stored %v", got)
		}
	})
	t.Run("no write on the NAR's path", func(t *testing.T) {
		f := newFixture(t)
		f.grant("folder", api.ActionRead|api.ActionWrite, "nar/**", "binary-cache/**/*.narinfo")
		status(t, f.do("PUT", prefix+f.info.URL, f.data, "publisher"), 201)
		wantBody(t, f.do("PUT", prefix+infoPath, f.meta, "publisher"), 403, `"message":"Forbidden"`)
		if f.has("local", storedInfo) || f.has("local", f.storedNAR()) {
			t.Fatalf("stored %v", f.files("local"))
		}
	})
	t.Run("write on both", func(t *testing.T) {
		f := newFixture(t)
		f.grant("all", api.ActionRead|api.ActionWrite, "nar/**", "binary-cache/**")
		status(t, f.do("PUT", prefix+f.info.URL, f.data, "publisher"), 201)
		status(t, f.do("PUT", prefix+infoPath, f.meta, "publisher"), 201)
		if got := f.files("local"); len(got) != 2 {
			t.Fatalf("stored %v", got)
		}
	})
}

// Read is checked where the files are stored, so a permission target
// written for the client's URLs no longer covers them, and one written
// for the binary-cache folder, as for 7.x, does.
func TestAccNixReadIsCheckedAtTheStoredPaths(t *testing.T) {
	f := newFixture(t)
	f.publish()
	f.grant("client", api.ActionRead, "nar/**", "*.narinfo")
	status(t, f.do("GET", prefix+infoPath, nil, "publisher"), 403)
	status(t, f.do("GET", f.native("local"), nil, "publisher"), 403)
	f.grant("folder", api.ActionRead, "binary-cache/**")
	status(t, f.do("GET", prefix+infoPath, nil, "publisher"), 200)
	status(t, f.do("GET", f.native("local"), nil, "publisher"), 200)
}

// DELETE names the files by the client's URLs and removes them where the
// repository stores them; a remote's cache is not deleted through them.
func TestAccNixDeleteRemovesTheStoredFiles(t *testing.T) {
	f, _, _, base := remoteFixture(t)
	f.publish()
	status(t, f.do("DELETE", base+infoPath, nil, ""), 404)
	status(t, f.do("DELETE", f.native("local"), nil, ""), 204)
	if f.has("local", f.storedNAR()) || !f.has("local", storedInfo) {
		t.Fatalf("stored %v", f.files("local"))
	}
	status(t, f.do("DELETE", prefix+"nar/"+infoPath[:32]+"/unknown", nil, ""), 404)
	status(t, f.do("DELETE", prefix+infoPath, nil, ""), 204)
	if got := f.files("local"); len(got) != 0 {
		t.Fatalf("stored %v", got)
	}
}

type publishFaults struct {
	port.ItemStore
	get       string
	checksums bool
}

func (s publishFaults) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (artmodel.Item, error) {
	if path == s.get {
		return artmodel.Item{}, io.ErrUnexpectedEOF
	}
	return s.ItemStore.Get(ctx, key, cached, path)
}

func (s publishFaults) FindByChecksum(
	ctx context.Context, c kernel.Checksums, p kernel.Page,
) ([]artmodel.Item, string, error) {
	if s.checksums {
		return nil, "", io.ErrUnexpectedEOF
	}
	return s.ItemStore.FindByChecksum(ctx, c, p)
}

type statFaults struct{ blob.Store }

func (statFaults) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{}, io.ErrUnexpectedEOF
}

// A fault while placing the NAR fails the publication with 500, never as a
// missing NAR's 409.
func TestAccNixPublishFaultsAreNotMisses(t *testing.T) {
	for name, tc := range map[string]struct {
		faults publishFaults
		stat   bool
		held   bool
	}{
		"stored archive lookup": {faults: publishFaults{get: "binary-cache/" + infoPath[:32] + "/18b3x2jida0r524p8g2wpimc4dqp8qhbhcy36g160lqx0hbqhp40.nar.xz"}, held: true},
		"held upload lookup":    {faults: publishFaults{get: "nar/18b3x2jida0r524p8g2wpimc4dqp8qhbhcy36g160lqx0hbqhp40.nar.xz"}},
		"stored archive search": {faults: publishFaults{checksums: true}},
		"held content":          {stat: true, held: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if tc.held {
				status(t, f.do("PUT", prefix+f.info.URL, f.data, ""), 201)
			}
			tc.faults.ItemStore = f.db.Items()
			f.st = failStore{serverStore: f.db, items: tc.faults}
			if tc.stat {
				f.items.Blobs = statFaults{f.items.Blobs}
			}
			f.mount(nil)
			status(t, f.do("PUT", prefix+infoPath, f.meta, ""), 500)
		})
	}
	f := newFixture(t)
	r := httptest.NewRequest("PUT", prefix+infoPath, errorBody{bytes.NewReader(nil)})
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	status(t, w, 500)
}
