package swift

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitSwiftArchiveRejectsUnsafeMembersAndCanceledInspection(t *testing.T) {
	good := archive(t, map[string]string{"Package.swift": manifest})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := InspectArchive(
		ctx,
		bytes.NewReader(good),
		int64(len(good)),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled inspection continued: %v", err)
	}
	if _, err := InspectArchive(t.Context(), bytes.NewReader(good), MaxArchiveBytes+1); err == nil {
		t.Fatal("oversized declared archive accepted")
	}
	for name, data := range map[string][]byte{
		"unnormalized member": archive(t, map[string]string{
			"Package.swift": manifest, "./Sources/Widget.swift": "x",
		}),
		"missing tools version": archive(t, map[string]string{
			"Package.swift": "import PackageDescription\nlet package = Package(name: \"Widget\")",
		}),
		"large manifest": archive(t, map[string]string{
			"Package.swift": manifest + strings.Repeat(" ", maxManifest),
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectArchive(
				t.Context(),
				bytes.NewReader(data),
				int64(len(data)),
			); err == nil {
				t.Fatal("unsafe Swift source archive accepted")
			}
		})
	}
	var symlink bytes.Buffer
	zw := zip.NewWriter(&symlink)
	manifestPart, err := zw.Create("Package.swift")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(manifestPart, manifest); err != nil {
		t.Fatal(err)
	}
	link := &zip.FileHeader{Name: "Sources/Widget/linked.swift", Method: zip.Deflate}
	link.SetMode(os.ModeSymlink | 0o777)
	linkPart, err := zw.CreateHeader(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(linkPart, "../../secret"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectArchive(
		t.Context(),
		bytes.NewReader(symlink.Bytes()),
		int64(symlink.Len()),
	); err == nil {
		t.Fatal("source ZIP symlink accepted")
	}
	var duplicate bytes.Buffer
	dupWriter := zip.NewWriter(&duplicate)
	for range 2 {
		part, err := dupWriter.Create("Package.swift")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, manifest); err != nil {
			t.Fatal(err)
		}
	}
	if err := dupWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectArchive(t.Context(), bytes.NewReader(duplicate.Bytes()),
		int64(duplicate.Len())); err == nil {
		t.Fatal("duplicate Swift manifest accepted")
	}
}

func TestUnitSwiftArchiveBoundsAndChecksEveryMember(t *testing.T) {
	data := archive(t, map[string]string{
		"Widget/Package.swift":               manifest,
		"Widget/Sources/Widget/Widget.swift": "public struct Widget {}",
	})
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	a := &Archive{Manifests: map[string][]byte{}, Tools: map[string]string{}}
	if _, err := inspectMember(
		t.Context(),
		reader.File[0],
		"Widget/",
		map[string]bool{},
		0,
		a,
	); err == nil ||
		!strings.Contains(err.Error(), "expanded size limit") {
		t.Fatalf("member exceeded remaining expansion budget: %v", err)
	}
	reader.File[0].CRC32 ^= 1
	if _, _, err := readMember(reader.File[0], true, maxExpanded); err == nil ||
		!strings.Contains(err.Error(), "corrupt ZIP member") {
		t.Fatalf("member with false CRC was accepted: %v", err)
	}
	reader.File[0].Method = 99
	if _, _, err := readMember(reader.File[0], true, maxExpanded); err == nil ||
		!strings.Contains(err.Error(), "open ZIP member") {
		t.Fatalf("unsupported ZIP compression was accepted: %v", err)
	}
	var directory bytes.Buffer
	zw := zip.NewWriter(&directory)
	if _, err := zw.Create("Widget/"); err != nil {
		t.Fatal(err)
	}
	part, err := zw.Create("Widget/Package.swift")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, manifest); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectArchive(t.Context(), bytes.NewReader(directory.Bytes()),
		int64(directory.Len())); err != nil {
		t.Fatalf("valid ZIP with an explicit package directory: %v", err)
	}
	missingTempDir := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", missingTempDir)
	t.Setenv("TMP", missingTempDir)
	t.Setenv("TEMP", missingTempDir)
	if _, err := InspectArchive(
		t.Context(),
		bytes.NewReader(data),
		int64(len(data)),
	); err == nil ||
		!strings.Contains(err.Error(), "temporary archive") {
		t.Fatalf("unavailable temporary storage did not fail closed: %v", err)
	}
}

type swiftCoverageBlob struct{ *bytes.Reader }

func (*swiftCoverageBlob) Close() error { return nil }

type swiftCoverageBlobs map[string][]byte

func (b swiftCoverageBlobs) Open(
	_ context.Context,
	digest string,
) (io.ReadSeekCloser, blob.Info, error) {
	data, ok := b[digest]
	if !ok {
		return nil, blob.Info{}, store.ErrNotFound
	}
	return &swiftCoverageBlob{bytes.NewReader(data)}, blob.Info{Size: int64(len(data))}, nil
}

func (b swiftCoverageBlobs) Stat(_ context.Context, digest string) (blob.Info, error) {
	data, ok := b[digest]
	if !ok {
		return blob.Info{}, store.ErrNotFound
	}
	return blob.Info{Size: int64(len(data))}, nil
}

func swiftCoverageSource(t *testing.T, b swiftCoverageBlobs, content string) (string, int64) {
	t.Helper()
	data := archive(t, map[string]string{
		"Package.swift":               manifest,
		"Sources/Widget/Widget.swift": content,
	})
	digest := sha256.Sum256(data)
	key := hex.EncodeToString(digest[:])
	b[key] = data
	return key, int64(len(data))
}

func swiftCoverageItem(
	t *testing.T,
	st *inmem.Store,
	c repo.Config,
	p Package,
	sha string,
	size int64,
) types.Item {
	t.Helper()
	it, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key,
		Path:    p.Scope + "/" + p.Name,
		Name:    p.Name + "-" + p.Version + ".zip",
		Size:    size,
		Actual:  types.Checksums{SHA256: sha},
		Created: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestUnitSwiftAdmissionRejectsCorruptAndMutableArchives(t *testing.T) {
	ctx := t.Context()
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "swift-local", Class: repo.Local, PackageType: repo.Swift})
	p := Package{Scope: "Acme", Name: "Widget", Version: "1.2.3"}
	st := inmem.New()
	blobs := swiftCoverageBlobs{}
	goodSHA, goodSize := swiftCoverageSource(t, blobs, "public struct Widget {}")
	oldSHA, oldSize := swiftCoverageSource(t, blobs, "public struct Widget { let old = true }")
	bad := []byte("not a Swift source ZIP")
	badDigest := sha256.Sum256(bad)
	badSHA := hex.EncodeToString(badDigest[:])
	blobs[badSHA] = bad
	env := format.Env{Store: st, Blobs: blobs}
	w := format.Write{
		Op: format.OpDeploy, Path: p.ArchivePath(), Size: goodSize,
		Checksums: types.Checksums{SHA256: goodSHA},
	}

	for _, skip := range []format.Write{
		{Op: format.OpChecksum, Path: p.ArchivePath()},
		{Op: format.OpDeploy, Path: "README.md"},
	} {
		got, err := h.Inspect(ctx, env, c, skip)
		if err != nil || got != (inspection{}) {
			t.Fatalf("non-archive inspection %q: %#v, %v", skip.Path, got, err)
		}
	}
	if _, err := h.Admit(ctx, env, c, format.Write{
		Op:   format.OpChecksum,
		Path: p.ArchivePath(),
	}); err != nil {
		t.Fatalf("checksum sidecar for existing Swift archive: %v", err)
	}
	if _, err := h.Inspect(ctx, env, c, format.Write{
		Op:   format.OpDeploy,
		Path: p.ArchivePath(), Checksums: types.Checksums{SHA256: strings.Repeat("f", 64)},
	}); err == nil {
		t.Fatal("missing source blob accepted")
	}
	if _, err := h.Inspect(ctx, env, c, format.Write{
		Op:   format.OpDeploy,
		Path: p.ArchivePath(), Size: int64(len(bad)),
		Checksums: types.Checksums{SHA256: badSHA},
	}); err == nil {
		t.Fatal("corrupt source ZIP accepted")
	}
	inspected, err := h.Inspect(ctx, env, c, w)
	if err != nil || inspected != (inspection{path: w.Path, sha: goodSHA}) {
		t.Fatalf("valid source inspection: %#v, %v", inspected, err)
	}
	env.Inspection = inspected
	if _, err := h.Admit(ctx, env, c, w); err != nil {
		t.Fatalf("new immutable release: %v", err)
	}
	if _, err := h.Admit(ctx, env, c, format.Write{
		Op:   format.OpDeploy,
		Path: "Acme/Widget/other.zip",
	}); err == nil {
		t.Fatal("noncanonical release path accepted")
	}
	blackout := true
	blocked := c
	blocked.BlackedOut = &blackout
	if _, err := h.Admit(ctx, env, blocked, w); err == nil {
		t.Fatal("blacked-out repository admitted a Swift release")
	}
	if _, err := h.Admit(ctx, format.Env{Store: st, Blobs: blobs}, c,
		format.Write{
			Op: format.OpDeploy, Path: p.ArchivePath(), Size: int64(len(bad)),
			Checksums: types.Checksums{SHA256: badSHA},
		}); err == nil {
		t.Fatal("corrupt copy or restore admitted without prior inspection")
	}
	old := swiftCoverageItem(t, st, c, p, oldSHA, oldSize)
	if _, err := h.Admit(ctx, env, c, w); err == nil {
		t.Fatal("different bytes replaced an immutable Swift release")
	}
	w.Checksums.SHA256, w.Size = oldSHA, oldSize
	if _, err := h.Admit(ctx, env, c, w); err != nil {
		t.Fatalf("idempotent same-content deploy: %v", err)
	}
	if err := h.Deployed(ctx, env, c, old); err != nil {
		t.Fatalf("index source archive: %v", err)
	}
	for _, unrelated := range []types.Item{
		{RepoKey: c.Key, Folder: true, Name: old.Name},
		{RepoKey: c.Key, Cached: true, Path: old.Path, Name: old.Name},
		{RepoKey: c.Key, Name: "README.md"},
	} {
		if err := h.Deployed(ctx, env, c, unrelated); err != nil {
			t.Fatalf("unrelated item changed Swift index: %v", err)
		}
	}
	if err := h.Deleting(ctx, env, c, old); err != nil {
		t.Fatalf("pre-delete hook: %v", err)
	}
	if err := h.Deleted(ctx, env, c, []types.Item{old}); err != nil {
		t.Fatalf("post-delete hook: %v", err)
	}
	if h.Classify(c, p.ArchivePath()) != format.ClassUnique ||
		!Serves(repo.Swift) || Serves(repo.NPM) {
		t.Fatal("Swift archive ownership or type boundary changed")
	}
	indexed, err := st.Packages().Find(ctx, c.Key, p.ID())
	if err != nil {
		t.Fatalf("published Swift package missing: %v", err)
	}
	versions, err := st.Packages().Versions(ctx, indexed.ID)
	if err != nil || len(versions) != 1 || versions[0].Version != p.Version {
		t.Fatalf("published Swift versions: %+v, %v", versions, err)
	}
}

func TestUnitSwiftReindexRestoresOnlyValidStoredArchives(t *testing.T) {
	ctx := t.Context()
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "swift-local", Class: repo.Local, PackageType: repo.Swift})
	st := inmem.New()
	blobs := swiftCoverageBlobs{}
	goodSHA, goodSize := swiftCoverageSource(t, blobs, "public struct Widget {}")
	p := Package{Scope: "Acme", Name: "Widget", Version: "1.2.3"}
	valid := swiftCoverageItem(t, st, c, p, goodSHA, goodSize)
	newer := Package{Scope: p.Scope, Name: p.Name, Version: "2.0.0"}
	_ = swiftCoverageItem(t, st, c, newer, goodSHA, goodSize)
	otherArchive := archive(t, map[string]string{
		"Package.swift":             strings.ReplaceAll(manifest, "Widget", "Other"),
		"Sources/Other/Other.swift": "public struct Other {}",
	})
	otherDigest := sha256.Sum256(otherArchive)
	otherSHA := hex.EncodeToString(otherDigest[:])
	blobs[otherSHA] = otherArchive
	other := Package{Scope: "Acme", Name: "Other", Version: "0.1.0"}
	_ = swiftCoverageItem(t, st, c, other, otherSHA, int64(len(otherArchive)))
	bad := []byte("not a ZIP")
	badDigest := sha256.Sum256(bad)
	badSHA := hex.EncodeToString(badDigest[:])
	blobs[badSHA] = bad
	invalid := Package{Scope: p.Scope, Name: p.Name, Version: "3.0.0"}
	_ = swiftCoverageItem(t, st, c, invalid, badSHA, int64(len(bad)))
	if _, err := st.Items().Put(ctx, types.Item{
		RepoKey: c.Key, Name: "README.md",
		Actual: types.Checksums{SHA256: goodSHA},
	}); err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: st, Blobs: blobs}
	if err := h.Reindex(ctx, env, c); err != nil {
		t.Fatalf("reindex valid and corrupt legacy items: %v", err)
	}
	indexed, err := st.Packages().Find(ctx, c.Key, p.ID())
	if err != nil {
		t.Fatalf("valid package omitted: %v", err)
	}
	versions, err := st.Packages().Versions(ctx, indexed.ID)
	if err != nil || len(versions) != 2 || versions[0].Version != p.Version ||
		versions[1].Version != newer.Version {
		t.Fatalf("corrupt legacy archive was advertised: %+v, %v", versions, err)
	}
	if _, err := st.Packages().Find(ctx, c.Key, other.ID()); err != nil {
		t.Fatalf("second valid package omitted during reindex: %v", err)
	}
	files, err := st.Packages().Files(ctx, versions[0].ID)
	if err != nil || len(files) != 1 || files[0] != valid.ID {
		t.Fatalf("valid archive link: %v, %v", files, err)
	}
	if err := h.Reindex(ctx, env, c); err != nil {
		t.Fatalf("idempotent reindex: %v", err)
	}
	missing := swiftCoverageBlobs{}
	if err := h.Reindex(ctx, format.Env{Store: st, Blobs: missing}, c); err == nil ||
		!errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing stored archive was silently advertised: %v", err)
	}
}
