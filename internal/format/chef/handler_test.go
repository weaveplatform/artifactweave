package chef

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
	manifest := []byte(`{"name":"acme-widget","version":"1.2.3","dependencies":{}}`)
	return archiveFixture(t, []tar.Header{
		{
			Name:     "acme-widget/metadata.json",
			Mode:     0o644,
			Size:     int64(len(manifest)),
			Typeflag: tar.TypeReg,
		},
	}, [][]byte{manifest})
}

func TestUnitAdmissionChecksArchiveIdentityAndRepositoryPolicy(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "chef-local", Class: repo.Local, PackageType: repo.Chef})
	archive := testArchive(t)
	env := format.Env{
		Blobs: testBlobs{content: map[string][]byte{"good": archive, "bad": []byte("bad")}},
	}
	path := Path("acme-widget", "1.2.3")
	write := format.Write{
		Op:        format.OpDeploy,
		Path:      path,
		Checksums: types.Checksums{SHA256: "good"},
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
		{"wrong path identity", format.Write{Op: format.OpDeploy, Path: Path("acme-other", "1.2.3"), Checksums: write.Checksums}, env, http.StatusConflict},
		{"noncanonical path", format.Write{Op: format.OpDeploy, Path: "acme-widget/../acme-widget-1.2.3.tar.gz", Checksums: write.Checksums}, env, http.StatusBadRequest},
		{"missing archive", format.Write{Op: format.OpDeploy, Path: path, Checksums: types.Checksums{SHA256: "missing"}}, env, 0},
		{"invalid archive", format.Write{Op: format.OpDeploy, Path: path, Checksums: types.Checksums{SHA256: "bad"}}, env, 0},
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
		{Op: format.OpDeploy, Cached: true, Path: "api/v1/cookbooks/acme-widget"},
	} {
		if _, err := h.Inspect(t.Context(), env, c, nonArchive); err != nil {
			t.Fatalf("non-archive inspection: %v", err)
		}
		if _, err := h.Admit(t.Context(), env, c, nonArchive); err != nil {
			t.Fatalf("non-archive admission: %v", err)
		}
	}
	if _, err := h.Admit(t.Context(), env, c, format.Write{
		Op: format.OpDeploy, Cached: true, Path: "cookbooks/invalid-path",
	}); err == nil {
		t.Fatal("invalid cached metadata name accepted")
	}
	blackout := c
	blackout.BlackedOut = new(bool)
	*blackout.BlackedOut = true
	if _, err := h.Admit(t.Context(), env, blackout, write); err == nil {
		t.Fatal("blacklisted repository accepted a cookbook write")
	}
}

func TestUnitDeployedAndReindexRejectStaleOrInvalidContent(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "chef-local", Class: repo.Local, PackageType: repo.Chef})
	st := inmem.New()
	env := format.Env{
		Store: st,
		Blobs: testBlobs{content: map[string][]byte{"good": testArchive(t)}},
	}
	if err := h.Deployed(
		t.Context(),
		env,
		c,
		types.Item{Folder: true, Name: "acme-widget"},
	); err != nil {
		t.Fatalf("folder should not publish a version: %v", err)
	}
	if err := h.Deployed(t.Context(), env, c, types.Item{Name: "README"}); err != nil {
		t.Fatalf("unrelated file should not publish a version: %v", err)
	}
	item := types.Item{
		RepoKey: c.Key, Path: "acme-other", Name: "acme-other-1.2.3.tar.gz",
		Actual: types.Checksums{SHA256: "good"},
	}
	if err := h.Deployed(t.Context(), env, c, item); !errors.Is(err, ErrArchive) {
		t.Fatalf("path/manifest mismatch: %v", err)
	}
	item.Path, item.Name = "acme-widget", "acme-widget-1.2.3.tar.gz"
	item.Actual.SHA256 = "missing"
	if err := h.Deployed(t.Context(), env, c, item); err == nil {
		t.Fatal("missing stored archive indexed")
	}
	if err := setProperties(t.Context(), st, types.Item{ID: 999}, Metadata{
		Name: "acme-widget", Version: "1.2.3", SHA256: "good",
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

func TestUnitMetadataTransferBoundaryAndUnsupportedAdmission(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "chef", Class: repo.Virtual, PackageType: repo.Chef})
	if _, err := h.Admit(
		t.Context(),
		format.Env{},
		c,
		format.Write{Op: format.OpDeploy, Path: Path("widget", "1.0")},
	); err == nil {
		t.Fatal("virtual direct write accepted")
	}
	for _, it := range []types.Item{{Path: "api/v1/cookbooks", Name: "widget"}, {Path: "widget", Name: "widget-1.0.tar.gz"}} {
		err := h.GuardTransferSource(t.Context(), format.Env{}, c, it)
		if (err != nil) != IsMetadata(it.FullPath()) {
			t.Fatalf("transfer boundary %s: %v", it.FullPath(), err)
		}
	}
}
