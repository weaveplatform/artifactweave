package cocoapods

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func storedPodArchive(t *testing.T, blobs blob.Store, name, version string) (string, int64) {
	t.Helper()
	spec := `{"name":"` + name + `","version":"` + version + `"}`
	data := packed(
		t,
		[]tar.Header{{Name: name + ".podspec.json", Typeflag: tar.TypeReg}},
		[]string{spec},
	)
	upload, err := blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(t.Context(), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	info, err := upload.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	return info.Digests.SHA256, info.Size
}

func TestUnitHandlerAdmissionChecksStoredArchives(t *testing.T) {
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := Handler{}
	c := repo.Config{Key: "pods-local", Class: repo.Local, PackageType: repo.CocoaPods}
	sha, _ := storedPodArchive(t, blobs, "HelloPod", "1.0.0")
	path := Path("HelloPod", "1.0.0")
	w := format.Write{Op: format.OpDeploy, Path: path, Checksums: types.Checksums{SHA256: sha}}
	env := format.Env{Blobs: blobs}
	if !Serves(repo.CocoaPods) || Serves(repo.NPM) {
		t.Fatal("CocoaPods handler claimed the wrong package type")
	}
	inspected, err := h.Inspect(t.Context(), env, c, w)
	if err != nil {
		t.Fatal(err)
	}
	env.Inspection = inspected
	if empty, err := h.Inspect(
		t.Context(),
		env,
		c,
		format.Write{Path: "README.md"},
	); err != nil ||
		empty != nil {
		t.Fatalf("unrelated item was inspected as a pod archive: %v, %v", empty, err)
	}
	if _, err := h.Admit(t.Context(), env, c, w); err != nil {
		t.Fatalf("valid inspected archive rejected: %v", err)
	}
	if _, err := h.Admit(
		t.Context(),
		env,
		c,
		format.Write{Op: format.OpChecksum, Path: path},
	); err != nil {
		t.Fatalf("checksum sidecar refused: %v", err)
	}
	blocked := c
	blackout := true
	blocked.BlackedOut = &blackout
	if _, err := h.Admit(t.Context(), env, blocked, w); err == nil {
		t.Fatal("blacked-out CocoaPods repository admitted a write")
	}
	if _, err := h.Admit(t.Context(), format.Env{Blobs: blobs}, c, w); err != nil {
		t.Fatalf("valid archive rejected without a reusable inspection: %v", err)
	}
	for _, tc := range []struct {
		name string
		w    format.Write
		env  format.Env
	}{
		{"generated cdn metadata", format.Write{Op: format.OpDeploy, Path: ".pod/cdn/all_pods.txt"}, env},
		{"ambiguous archive path", format.Write{Op: format.OpDeploy, Path: "HelloPod/bad.tar.gz"}, env},
		{"wrong archive identity", format.Write{Op: format.OpDeploy, Path: Path("OtherPod", "1.0.0"), Checksums: types.Checksums{SHA256: sha}}, format.Env{Blobs: blobs}},
		{"inspected identity mismatch", format.Write{Op: format.OpDeploy, Path: Path("OtherPod", "1.0.0"), Checksums: types.Checksums{SHA256: sha}}, env},
		{"missing blob", format.Write{Op: format.OpDeploy, Path: path, Checksums: types.Checksums{SHA256: strings.Repeat("f", 64)}}, format.Env{Blobs: blobs}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.Admit(t.Context(), tc.env, c, tc.w); err == nil {
				t.Fatal("unsafe CocoaPods write admitted")
			}
		})
	}
	if _, err := h.Inspect(
		t.Context(),
		env,
		c,
		format.Write{Path: path, Checksums: types.Checksums{SHA256: strings.Repeat("f", 64)}},
	); err == nil {
		t.Fatal("inspection did not report missing archive")
	}
	if _, err := h.Admit(
		t.Context(),
		env,
		c,
		format.Write{Op: format.OpDeploy, Path: "README.md"},
	); err != nil {
		t.Fatalf("unrelated item rejected: %v", err)
	}
}

func TestAccHandlerReindexRestoresPublishedVersions(t *testing.T) {
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := inmem.New()
	c := *repo.Defaults(&repo.Config{Key: "pods-local", Class: repo.Local, PackageType: repo.CocoaPods})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: db, Blobs: blobs}
	h := Handler{}
	for _, version := range []string{"1.0.0", "2.0.0"} {
		sha, size := storedPodArchive(t, blobs, "HelloPod", version)
		item, err := db.Items().Put(t.Context(), types.Item{
			RepoKey: c.Key, Name: Path("HelloPod", version), Size: size,
			Actual: types.Checksums{SHA256: sha},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Deployed(t.Context(), env, c, item); err != nil {
			t.Fatalf("publish %s: %v", version, err)
		}
	}
	indexed, err := db.Packages().Find(t.Context(), c.Key, "HelloPod")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Packages().Delete(t.Context(), indexed.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(t.Context(), env, c); err != nil {
		t.Fatalf("reindex stored archives: %v", err)
	}
	indexed, err = db.Packages().Find(t.Context(), c.Key, "HelloPod")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1.0.0", "2.0.0"} {
		if _, err := db.Packages().Version(t.Context(), indexed.ID, version); err != nil {
			t.Fatalf("reindex lost %s: %v", version, err)
		}
	}
	if err := h.Deployed(t.Context(), env, c, types.Item{Name: "README.md"}); err != nil {
		t.Fatalf("unrelated item affected CocoaPods index: %v", err)
	}
	bad := types.Item{
		Name:   Path("BrokenPod", "1.0.0"),
		Actual: types.Checksums{SHA256: strings.Repeat("f", 64)},
	}
	if err := h.Deployed(t.Context(), env, c, bad); err == nil {
		t.Fatal("missing uploaded archive was indexed")
	}
	corrupt, err := blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corrupt.Append(t.Context(), strings.NewReader("not a gzip archive")); err != nil {
		t.Fatal(err)
	}
	info, err := corrupt.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(t.Context(), env, c, types.Item{
		Name: Path("BrokenPod", "1.0.0"), Actual: types.Checksums{SHA256: info.Digests.SHA256},
	}); err == nil {
		t.Fatal("corrupt stored archive was indexed")
	}
	if err := h.Deployed(t.Context(), env, c, types.Item{
		Name: Path(
			"BrokenPod",
			"1.0.0",
		),
		Actual: types.Checksums{SHA256: strings.Repeat("f", 64)},
		Cached: true,
	}); err != nil {
		t.Fatalf("cached remote archive was indexed as a hosted pod: %v", err)
	}
	if _, err := db.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Name: bad.Name, Actual: bad.Actual,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(t.Context(), env, c); err == nil {
		t.Fatal("reindex ignored a missing stored archive")
	}
}
