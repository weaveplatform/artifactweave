package cran

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitCRANIndexOwnershipAndUploadPolicy(t *testing.T) {
	t.Parallel()
	h := Handler{}
	local := repo.Config{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran}
	remote := repo.Config{Key: "cran-remote", Class: repo.Remote, PackageType: repo.Cran}
	index := "src/contrib/PACKAGES.gz"
	if got := h.Classify(local, index); got != format.ClassDerived {
		t.Fatalf("index class = %v", got)
	}
	if got := h.Classify(local, "src/contrib/demo_1.0.tar.gz"); got != format.ClassUnique {
		t.Fatalf("package class = %v", got)
	}
	if got := h.Owed(local, false, index); len(got) != 1 ||
		got[0].Kind != KindIndex || got[0].Key != local.Key {
		t.Fatalf("local index work = %+v", got)
	}
	for _, tc := range []struct {
		name   string
		config repo.Config
		cached bool
		path   string
	}{
		{"cached local", local, true, index},
		{"remote index", remote, false, index},
		{"local package", local, false, "src/contrib/demo_1.0.tar.gz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.Owed(tc.config, tc.cached, tc.path); len(got) != 0 {
				t.Fatalf("unexpected work = %+v", got)
			}
		})
	}
	for _, tc := range []struct {
		name, path string
		config     repo.Config
		cached     bool
		status     int
	}{
		{"derived index", index, local, false, http.StatusConflict},
		{"invalid archive location", "src/contrib/bad/archive.tar.gz", local, false, http.StatusBadRequest},
		{"cached index", index, local, true, 0},
		{"remote index", index, remote, false, 0},
		{"ordinary metadata", "README", local, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Admit(t.Context(), format.Env{}, tc.config, format.Write{
				Op: format.OpDeploy, Path: tc.path, Cached: tc.cached,
			})
			var refused *format.RejectionError
			if tc.status == 0 && err != nil || tc.status != 0 &&
				(!errors.As(err, &refused) || refused.Status != tc.status) {
				t.Fatalf("admission error = %v, want status %d", err, tc.status)
			}
		})
	}
	_, err := h.Admit(t.Context(), format.Env{}, local, format.Write{
		Op: format.OpChecksum, Path: index,
	})
	if err != nil {
		t.Fatalf("checksum admission: %v", err)
	}
	local.BlackedOut = new(true)
	if _, err := h.Admit(t.Context(), format.Env{}, local, format.Write{
		Op: format.OpDeploy, Path: "src/contrib/demo_1.0.tar.gz",
	}); err == nil {
		t.Fatal("blacked-out CRAN repository accepted a package")
	}
}

func TestUnitCRANArchiveValidationRejectsUntrustedContent(t *testing.T) {
	t.Parallel()
	type archiveEntry struct {
		name, content string
		kind          byte
	}
	archive := func(t *testing.T, entries []archiveEntry) []byte {
		t.Helper()
		var body bytes.Buffer
		gz := gzip.NewWriter(&body)
		tw := tar.NewWriter(gz)
		for _, entry := range entries {
			kind := entry.kind
			if kind == 0 {
				kind = tar.TypeReg
			}
			h := &tar.Header{Name: entry.name, Typeflag: kind, Mode: 0o644}
			if kind == tar.TypeReg {
				h.Size = int64(len(entry.content))
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if kind == tar.TypeReg {
				if _, err := io.WriteString(tw, entry.content); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return body.Bytes()
	}
	const valid = "Package: demo\nVersion: 1.0\n"
	for _, tc := range []struct {
		name    string
		entries []archiveEntry
	}{
		{"missing DESCRIPTION", []archiveEntry{{name: "demo/README", content: "missing metadata"}}},
		{"duplicate DESCRIPTION", []archiveEntry{{name: "demo/DESCRIPTION", content: valid}, {name: "demo/DESCRIPTION", content: valid}}},
		{"symlink", []archiveEntry{{name: "demo/DESCRIPTION", content: valid}, {name: "demo/R/entry", kind: tar.TypeSymlink}}},
		{"different root", []archiveEntry{{name: "other/DESCRIPTION", content: valid}}},
		{"invalid DESCRIPTION", []archiveEntry{{name: "demo/DESCRIPTION", content: "Package: demo\nVersion: 1.0\nVersion: 2.0\n"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := archive(t, tc.entries)
			if _, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)),
				"src/contrib/demo_1.0.tar.gz"); !errors.Is(err, ErrPackage) {
				t.Fatalf("untrusted archive error = %v", err)
			}
		})
	}
	good := sourceArchive(t, "demo", "1.0", "demo/DESCRIPTION", true)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadPackage(canceled, bytes.NewReader(good), int64(len(good)),
		"src/contrib/demo_1.0.tar.gz"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled archive validation = %v", err)
	}
	for _, tc := range []struct {
		feed, suffix string
		body         []byte
	}{
		{"src/contrib", ".zip", good},
		{"other", ".tar.gz", good},
		{"src/contrib", ".tar.gz", []byte("not a package")},
	} {
		if _, err := ReadUploaded(t.Context(), bytes.NewReader(tc.body), int64(len(tc.body)),
			tc.feed, tc.suffix); !errors.Is(err, ErrPackage) {
			t.Fatalf("untrusted upload %q %q: %v", tc.feed, tc.suffix, err)
		}
	}
	if _, err := ReadPackage(t.Context(), bytes.NewReader([]byte("broken")), 6,
		"bin/windows/contrib/4.5/demo_1.0.zip"); !errors.Is(err, ErrPackage) {
		t.Fatalf("invalid ZIP = %v", err)
	}
	var zipBody bytes.Buffer
	zw := zip.NewWriter(&zipBody)
	w, err := zw.Create("demo/README")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "no DESCRIPTION"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(t.Context(), bytes.NewReader(zipBody.Bytes()), int64(zipBody.Len()),
		"bin/windows/contrib/4.5/demo_1.0.zip"); !errors.Is(err, ErrPackage) {
		t.Fatalf("ZIP without DESCRIPTION = %v", err)
	}
}

func TestUnitCRANArchivedXZAndBinaryIndexCompatibility(t *testing.T) {
	t.Parallel()
	var tarBody bytes.Buffer
	tw := tar.NewWriter(&tarBody)
	const description = "Package: demo\nVersion: 1.0\nDescription: compressed CRAN source\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: "demo/DESCRIPTION", Mode: 0o644, Size: int64(len(description)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, description); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	xzw, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xzw.Write(tarBody.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := xzw.Close(); err != nil {
		t.Fatal(err)
	}
	const archivePath = "src/contrib/Archive/demo/demo_1.0.tar.xz"
	pkg, err := ReadPackage(t.Context(), bytes.NewReader(compressed.Bytes()),
		int64(compressed.Len()), archivePath)
	if err != nil || pkg.Path != archivePath || pkg.Name != "demo" {
		t.Fatalf("archived xz source = %+v, %v", pkg, err)
	}
	if _, err := ReadPackage(t.Context(), bytes.NewReader([]byte("broken")), 6,
		archivePath); !errors.Is(err, ErrPackage) {
		t.Fatalf("invalid xz archive = %v", err)
	}
	for _, tc := range []struct {
		path, suffix string
	}{
		{"bin/windows/contrib/4.5/PACKAGES", ".zip"},
		{"bin/macosx/big-sur-arm64/contrib/4.5/PACKAGES", ".tgz"},
	} {
		body := []byte("Package: demo\nVersion: 1.0\nMD5sum: ABCDEF0123456789ABCDEF0123456789\n")
		got, err := DecodeIndex(body, tc.path)
		if err != nil || len(got) != 1 || !strings.HasSuffix(got[0].Path, tc.suffix) ||
			got[0].MD5 != "abcdef0123456789abcdef0123456789" {
			t.Fatalf("binary PACKAGES %s = %+v, %v", tc.path, got, err)
		}
	}
}

func TestUnitCRANDescriptionAndIndexTrustBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, description string
	}{
		{"missing identity", "Description: no identity\n"},
		{"duplicate package", "Package: demo\nPackage: other\nVersion: 1.0\n"},
		{"missing colon", "Package demo\nVersion: 1.0\n"},
		{"leading continuation", " continued\nPackage: demo\nVersion: 1.0\n"},
		{"invalid field key", "Package: demo\nVersion: 1.0\nBad key: unsafe\n"},
		{"carriage return", "Package: demo\nVersion: 1.0\nDescription: unsafe\rvalue\n"},
		{"nul byte", "Package: demo\nVersion: 1.0\nDescription: unsafe\x00value\n"},
		{"overlong value", "Package: demo\nVersion: 1.0\nDescription: " + strings.Repeat("a", 4097) + "\n"},
		{"empty field key", "Package: demo\nVersion: 1.0\n: empty\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFields([]byte(tc.description)); !errors.Is(err, ErrPackage) {
				t.Fatalf("malformed DESCRIPTION error = %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		pkg  Package
		feed string
	}{
		{"invalid feed", Package{}, "other"},
		{"mismatched path", Package{Name: "demo", Version: "1.0", Feed: "src/contrib", Path: "src/contrib/other_1.0.tar.gz"}, "src/contrib"},
		{"untrusted field", Package{Name: "demo", Version: "1.0", Feed: "src/contrib", Path: "src/contrib/demo_1.0.tar.gz", Fields: map[string]string{"License": "MIT\nInjected: value"}}, "src/contrib"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EncodeIndex([]Package{tc.pkg}, tc.feed); !errors.Is(err, ErrPackage) {
				t.Fatalf("index publication error = %v", err)
			}
		})
	}
	for _, tc := range []struct {
		a, b     string
		expected int
	}{
		{"1.9", "1.10", -1},
		{"2.0", "1.99", 1},
		{"1", "1.0", -1},
		{"1.0", "1", 1},
		{"1.0", "1.0", 0},
		{"1a", "1b", -1},
	} {
		got := CompareVersion(tc.a, tc.b)
		if got != tc.expected {
			t.Errorf("version order %q vs %q = %d, want %d", tc.a, tc.b, got, tc.expected)
		}
	}
	for _, tc := range []struct {
		path string
		size int64
	}{
		{"src/contrib/demo_1.0.tar.gz", 0},
		{"src/contrib/other_1.0.zip", 42},
	} {
		if _, err := ReadPackage(t.Context(), bytes.NewReader([]byte("invalid")), tc.size,
			tc.path); !errors.Is(err, ErrPackage) {
			t.Fatalf("invalid package identity error = %v", err)
		}
	}
}

func TestUnitCRANLifecycleDoesNotQueueCachedOrRemoteContent(t *testing.T) {
	t.Parallel()
	h := Handler{}
	local := repo.Config{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran}
	remote := repo.Config{Key: "cran-remote", Class: repo.Remote, PackageType: repo.Cran}
	var queued []string
	env := format.Env{Enqueue: func(_ context.Context, kind, key string, _ []byte) error {
		queued = append(queued, kind+":"+key)
		return nil
	}}
	for _, tc := range []struct {
		config repo.Config
		item   types.Item
	}{
		{local, types.Item{Folder: true}},
		{local, types.Item{Cached: true}},
		{remote, types.Item{}},
	} {
		if err := h.Deleting(t.Context(), env, tc.config, tc.item); err != nil {
			t.Fatal(err)
		}
	}
	if len(queued) != 0 {
		t.Fatalf("unexpected index work: %v", queued)
	}
	if err := queue(t.Context(), env, local, false); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0] != KindIndex+":"+local.Key {
		t.Fatalf("queued work = %v", queued)
	}
	if got := h.WorkKinds(); len(got) != 1 || got[0] != KindIndex {
		t.Fatalf("work kinds = %v", got)
	}
	if err := h.Work(t.Context(), format.Env{}, types.WorkItem{Kind: "unrelated"}); err != nil {
		t.Fatalf("unrelated work: %v", err)
	}
	if got, err := h.Inspect(t.Context(), format.Env{}, local, format.Write{
		Op: format.OpChecksum, Path: "src/contrib/demo_1.0.tar.gz",
	}); err != nil || got == nil {
		t.Fatalf("checksum inspection = %v, %v", got, err)
	}
	if _, err := readStored(t.Context(), nil, "", "src/contrib/demo_1.0.tar.gz"); err == nil {
		t.Fatal("missing package content was admitted")
	}
	if got := indexedVersion("1.0", nil); got.Version != "1.0" || len(got.Files) != 0 {
		t.Fatalf("empty version = %+v", got)
	}
}

func TestUnitCRANRejectsUntrustedIndexRecords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
	}{
		{"path escape", "Package: demo\nVersion: 1.0\nFile: ../demo_1.0.tar.gz\n"},
		{"other identity", "Package: demo\nVersion: 1.0\nFile: other_1.0.tar.gz\n"},
		{"invalid digest", "Package: demo\nVersion: 1.0\nMD5sum: not-a-digest\n"},
		{"nonhex digest", "Package: demo\nVersion: 1.0\nMD5sum: zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz\n"},
		{"invalid subfolder", "Package: demo\nVersion: 1.0\nPath: ../private\n"},
		{"absolute subfolder", "Package: demo\nVersion: 1.0\nPath: /private\n"},
		{"duplicate package", "Package: demo\nVersion: 1.0\n\nPackage: demo\nVersion: 2.0\n"},
		{"duplicate field", "Package: demo\nVersion: 1.0\nVersion: 2.0\n"},
		{"orphan continuation", " orphan\nPackage: demo\nVersion: 1.0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeIndex(
				[]byte(tc.body),
				"src/contrib/PACKAGES",
			); !errors.Is(
				err,
				ErrPackage,
			) {
				t.Fatalf("malformed upstream index error = %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name, path string
		body       []byte
	}{
		{"bad route", "src/contrib/metadata", []byte("Package: demo\nVersion: 1.0\n")},
		{"bad gzip", "src/contrib/PACKAGES.gz", []byte("not gzip")},
		{"oversized", "src/contrib/PACKAGES", bytes.Repeat([]byte{'x'}, maxIndexBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeIndex(tc.body, tc.path); !errors.Is(err, ErrPackage) {
				t.Fatalf("invalid index error = %v", err)
			}
		})
	}
}

func TestUnitCRANRejectsDamagedCompressedIndexesAndCanceledZIP(t *testing.T) {
	t.Parallel()
	encoded, err := EncodeIndex([]Package{{
		Name: "demo", Version: "1.0", Feed: "src/contrib",
		Path: "src/contrib/demo_1.0.tar.gz",
	}}, "src/contrib")
	if err != nil {
		t.Fatal(err)
	}
	gz := encoded[PackagesGzipName]
	if _, err := DecodeIndex(
		gz[:len(gz)-6],
		"src/contrib/PACKAGES.gz",
	); !errors.Is(
		err,
		ErrPackage,
	) {
		t.Fatalf("truncated gzip index = %v", err)
	}
	body := sourceArchive(t, "demo", "1.0", "demo/DESCRIPTION", true)
	if _, err := ReadUploaded(t.Context(), bytes.NewReader(body), int64(len(body)),
		"src/contrib", ".tar.gz"); err != nil {
		t.Fatalf("valid native POST source was rejected: %v", err)
	}
	zipBody, err := os.ReadFile("testdata/assertthat_0.2.1.zip")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadPackage(ctx, bytes.NewReader(zipBody), int64(len(zipBody)),
		"bin/windows/contrib/4.5/assertthat_0.2.1.zip"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ZIP validation = %v", err)
	}
}
