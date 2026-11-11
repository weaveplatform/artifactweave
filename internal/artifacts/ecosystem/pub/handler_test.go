package pub

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

type (
	testBlobs      struct{ content map[string][]byte }
	testBlobReader struct{ *bytes.Reader }
)

func (testBlobReader) Close() error { return nil }

func (b testBlobs) Open(_ context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	data, ok := b.content[sha]
	if !ok {
		return nil, blob.Info{}, errors.New("blob missing")
	}
	return testBlobReader{bytes.NewReader(data)}, blob.Info{Size: int64(len(data))}, nil
}

func (b testBlobs) Stat(_ context.Context, sha string) (blob.Info, error) {
	data, ok := b.content[sha]
	if !ok {
		return blob.Info{}, errors.New("blob missing")
	}
	return blob.Info{Size: int64(len(data))}, nil
}

func testArchive(t *testing.T) []byte {
	t.Helper()
	manifest := []byte("name: widget_pub\nversion: 1.2.3\n")
	return archiveFixture(t, []tar.Header{{
		Name: "pubspec.yaml", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg,
	}}, [][]byte{manifest})
}

func TestUnitAdmissionChecksArchiveIdentityAndRepositoryPolicy(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "pub-local", Class: repo.Local, PackageType: repo.Pub})
	archive := testArchive(t)
	env := format.Env{
		Blobs: testBlobs{content: map[string][]byte{"good": archive, "bad": []byte("bad")}},
	}
	path := Path("widget_pub", "1.2.3")
	write := format.Write{
		Op:        format.OpDeploy,
		Path:      path,
		Checksums: kernel.Checksums{SHA256: "good"},
	}
	inspected, err := h.Inspect(t.Context(), env, c, write)
	if err != nil {
		t.Fatalf("valid archive inspection: %v", err)
	}
	env.Inspection = inspected
	if _, err := h.Admit(t.Context(), env, c, write); err != nil {
		t.Fatalf("matching identity refused: %v", err)
	}
	for _, tc := range []struct {
		name  string
		write format.Write
		env   format.Env
		code  int
	}{
		{"wrong path identity", format.Write{Op: format.OpDeploy, Path: Path("other_pub", "1.2.3"), Checksums: write.Checksums}, env, http.StatusConflict},
		{"noncanonical path", format.Write{Op: format.OpDeploy, Path: "widget_pub/../widget_pub-1.2.3.tar.gz", Checksums: write.Checksums}, env, http.StatusBadRequest},
		{"missing archive", format.Write{Op: format.OpDeploy, Path: path, Checksums: kernel.Checksums{SHA256: "missing"}}, env, 0},
		{"invalid archive", format.Write{Op: format.OpDeploy, Path: path, Checksums: kernel.Checksums{SHA256: "bad"}}, env, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Admit(t.Context(), tc.env, c, tc.write)
			if err == nil {
				t.Fatal("unsafe write admitted")
			}
			var rejected *format.RejectionError
			if tc.code != 0 && (!errors.As(err, &rejected) || rejected.Status != tc.code) {
				t.Fatalf("write refusal = %v, want HTTP %d", err, tc.code)
			}
		})
	}
	for _, sha := range []string{"missing", "bad"} {
		write.Checksums.SHA256 = sha
		if _, err := h.Inspect(t.Context(), env, c, write); err == nil {
			t.Errorf("inspected %q as valid archive", sha)
		}
	}
	for _, nonArchive := range []format.Write{
		{Op: format.OpChecksum, Path: path},
		{Op: format.OpDeploy, Cached: true, Path: "api/packages/widget_pub"},
	} {
		if _, err := h.Inspect(t.Context(), env, c, nonArchive); err != nil {
			t.Fatalf("non-archive inspection: %v", err)
		}
		if _, err := h.Admit(t.Context(), env, c, nonArchive); err != nil {
			t.Fatalf("non-archive admission: %v", err)
		}
	}
	if _, err := h.Admit(t.Context(), env, c, format.Write{
		Op: format.OpDeploy, Cached: true, Path: "api/packages/Bad-Name",
	}); err == nil {
		t.Fatal("invalid cached metadata name accepted")
	}
	blackout := c
	blackout.BlackedOut = new(bool)
	*blackout.BlackedOut = true
	if _, err := h.Admit(t.Context(), env, blackout, write); err == nil {
		t.Fatal("blacklisted repository accepted a pub write")
	}
}

func TestUnitDeployedAndReindexRejectStaleOrInvalidContent(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "pub-local", Class: repo.Local, PackageType: repo.Pub})
	st := inmem.New()
	env := format.Env{
		Store: st,
		Blobs: testBlobs{content: map[string][]byte{"good": testArchive(t)}},
	}
	if err := h.Deployed(
		t.Context(),
		env,
		c,
		model.Item{Folder: true, Name: "widget_pub"},
	); err != nil {
		t.Fatalf("folder should not publish a version: %v", err)
	}
	if err := h.Deployed(t.Context(), env, c, model.Item{Name: "README"}); err != nil {
		t.Fatalf("unrelated file should not publish a version: %v", err)
	}
	item := model.Item{
		RepoKey: c.Key, Path: "other_pub", Name: "other_pub-1.2.3.tar.gz",
		Actual: kernel.Checksums{SHA256: "good"},
	}
	if err := h.Deployed(t.Context(), env, c, item); !errors.Is(err, ErrArchive) {
		t.Fatalf("path/manifest mismatch: %v", err)
	}
	item.Path, item.Name = "widget_pub", "widget_pub-1.2.3.tar.gz"
	item.Actual.SHA256 = "missing"
	if err := h.Deployed(t.Context(), env, c, item); err == nil {
		t.Fatal("missing stored archive indexed")
	}
	if err := setProperties(t.Context(), st, model.Item{ID: 999}, Metadata{
		Name: "widget_pub", Version: "1.2.3", SHA256: "good",
	}); err == nil {
		t.Fatal("missing item properties changed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.Reindex(ctx, env, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reindex: %v", err)
	}
	refused := errors.New("reindex permission revoked")
	env.CheckReindex = func(context.Context) error { return refused }
	if err := h.Reindex(t.Context(), env, c); !errors.Is(err, refused) {
		t.Fatalf("reindex did not recheck authority: %v", err)
	}
}

// TestUnitGuardWriteRefusesOnlyAPublishedVersion: a second write to the path
// of a stored version is refused with 409, while a checksum deploy, a
// remote's cached content and a version nothing holds pass the guard. A
// cached archive still has its identity checked at admission.
func TestUnitGuardWriteRefusesOnlyAPublishedVersion(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "pub-local", Class: repo.Local, PackageType: repo.Pub})
	st := inmem.New()
	path := Path("widget_pub", "1.2.3")
	if _, err := st.Items().Put(t.Context(), model.Item{
		RepoKey: c.Key, Name: path, Actual: kernel.Checksums{SHA256: "good"},
	}); err != nil {
		t.Fatal(err)
	}
	env := format.Env{
		Store: st,
		Blobs: testBlobs{content: map[string][]byte{"good": testArchive(t)}},
	}
	for _, tc := range []struct {
		name  string
		write format.Write
		code  int
	}{
		{"published version", format.Write{Op: format.OpDeploy, Path: path}, http.StatusConflict},
		{"checksum deploy", format.Write{Op: format.OpChecksum, Path: path}, 0},
		{"cached content", format.Write{Op: format.OpDeploy, Path: path, Cached: true}, 0},
		{"new version", format.Write{Op: format.OpDeploy, Path: Path("widget_pub", "1.2.4")}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := h.GuardWrite(t.Context(), env, c, tc.write)
			var rejected *format.RejectionError
			switch {
			case tc.code == 0 && err != nil:
				t.Fatalf("guard refused: %v", err)
			case tc.code != 0 && (!errors.As(err, &rejected) || rejected.Status != tc.code ||
				rejected.Message != "Dart pub version already published"):
				t.Fatalf("guard answer = %v, want HTTP %d", err, tc.code)
			}
		})
	}
	cached := format.Write{
		Op: format.OpDeploy, Cached: true, Path: path,
		Checksums: kernel.Checksums{SHA256: "good"},
	}
	if _, err := h.Admit(t.Context(), env, c, cached); err != nil {
		t.Fatalf("cached archive matching its path refused: %v", err)
	}
	cached.Path = Path("other_pub", "1.2.3")
	var rejected *format.RejectionError
	if _, err := h.Admit(t.Context(), env, c, cached); !errors.As(err, &rejected) ||
		rejected.Status != http.StatusConflict {
		t.Fatalf("cached archive under another identity: %v, want HTTP 409", err)
	}
}

// TestUnitReindexRebuildsVersionsFromStoredArchives: Reindex indexes each
// stored archive as a version of its package, with the archive's path and
// SHA-256 in the version's metadata and the pub.* properties on the item,
// passes over folders and other files, leaves the properties alone when the
// environment skips them, and refuses an archive whose pubspec no longer
// matches its path. Deployed fails when it cannot read the item's
// properties, and the delete hooks change nothing.
func TestUnitReindexRebuildsVersionsFromStoredArchives(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "pub-local", Class: repo.Local, PackageType: repo.Pub})
	st := inmem.New()
	ctx := t.Context()
	env := format.Env{
		Store: st,
		Blobs: testBlobs{content: map[string][]byte{"good": testArchive(t)}},
	}
	path := Path("widget_pub", "1.2.3")
	for _, it := range []model.Item{
		{RepoKey: c.Key, Name: "widget_pub", Folder: true},
		{RepoKey: c.Key, Name: "widget_pub/README.md", Actual: kernel.Checksums{SHA256: "readme"}},
		{RepoKey: c.Key, Name: path, Actual: kernel.Checksums{SHA256: "good"}},
	} {
		if _, err := st.Items().Put(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := st.Items().Get(ctx, c.Key, false, path)
	if err != nil {
		t.Fatal(err)
	}
	indexed := func(t *testing.T) {
		t.Helper()
		packages, _, err := st.Packages().List(ctx, c.Key, kernel.Page{Limit: kernel.MaxLimit})
		if err != nil || len(packages) != 1 || packages[0].Name != "widget_pub" {
			t.Fatalf("packages: %+v, %v", packages, err)
		}
		versions, err := st.Packages().Versions(ctx, packages[0].ID)
		if err != nil || len(versions) != 1 || versions[0].Version != "1.2.3" {
			t.Fatalf("versions: %+v, %v", versions, err)
		}
		var meta Metadata
		if err := json.Unmarshal(versions[0].Metadata, &meta); err != nil ||
			meta.Name != "widget_pub" || meta.Version != "1.2.3" || meta.Path != path ||
			meta.SHA256 != "good" || meta.Pubspec["name"] != "widget_pub" {
			t.Fatalf("version metadata %s: %v", versions[0].Metadata, err)
		}
		files, err := st.Packages().Files(ctx, versions[0].ID)
		if err != nil || len(files) != 1 || files[0] != stored.ID {
			t.Fatalf("version files: %v, %v", files, err)
		}
	}
	properties := func(t *testing.T) model.Properties {
		t.Helper()
		props, err := st.Items().Properties(ctx, stored.ID)
		if err != nil {
			t.Fatal(err)
		}
		return props
	}
	want := model.Properties{
		PropName: {"widget_pub"}, PropVersion: {"1.2.3"}, PropSHA256: {"good"},
	}
	// The second run finds the properties already set and writes none.
	for range 2 {
		if err := h.Reindex(ctx, env, c); err != nil {
			t.Fatalf("reindex: %v", err)
		}
		indexed(t)
		if got := properties(t); !maps.EqualFunc(got, want, slices.Equal) {
			t.Fatalf("archive properties: %v, want %v", got, want)
		}
	}
	if err := st.Items().SetProperties(ctx, stored.ID, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	skipping := env
	skipping.SkipProperties = true
	if err := h.Reindex(ctx, skipping, c); err != nil {
		t.Fatalf("reindex without properties: %v", err)
	}
	indexed(t)
	if got := properties(t); len(got) != 0 {
		t.Fatalf("reindex that skips properties set %v", got)
	}
	for _, hook := range []func() error{
		func() error { return h.Deleting(ctx, env, c, stored) },
		func() error { return h.Deleted(ctx, env, c, []model.Item{stored}) },
	} {
		if err := hook(); err != nil {
			t.Fatalf("delete hook: %v", err)
		}
	}
	indexed(t)
	unstored := model.Item{
		ID: stored.ID + 100, RepoKey: c.Key, Path: "widget_pub", Name: "widget_pub-1.2.3.tar.gz",
		Actual: kernel.Checksums{SHA256: "good"},
	}
	if err := h.Deployed(ctx, env, c, unstored); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("deployed archive without an item: %v", err)
	}
	stale := Path("other_pub", "1.2.3")
	if _, err := st.Items().Put(ctx, model.Item{
		RepoKey: c.Key, Name: stale, Actual: kernel.Checksums{SHA256: "good"},
	}); err != nil {
		t.Fatal(err)
	}
	err = h.Reindex(ctx, env, c)
	if !errors.Is(err, ErrArchive) ||
		err.Error() != "pub: reindex archive "+stale+": pub: invalid dart pub archive" {
		t.Fatalf("reindex of an archive under another identity: %v", err)
	}
}
