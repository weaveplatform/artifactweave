package composer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func archive(t *testing.T, metadata string, extra map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("composer.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte(metadata))
	for name, contents := range extra {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(contents))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitParseArchiveIdentityAndBounds(t *testing.T) {
	good := archive(
		t,
		`{"name":"acme/widget","version":"1.2.3","require":{"php":"^8.0"}}`,
		map[string]string{"src/Widget.php": "<?php"},
	)
	p, err := ParseArchive(context.Background(), bytes.NewReader(good), int64(len(good)), "")
	if err != nil || p.Name != "acme/widget" || p.Version != "1.2.3" ||
		p.Document()["require"] == nil {
		t.Fatalf("package %+v: %v", p, err)
	}
	for _, tc := range []struct {
		name     string
		data     []byte
		size     int64
		override string
	}{
		{"missing version", archive(t, `{"name":"acme/widget"}`, nil), 0, ""},
		{"bad name", archive(t, `{"name":"../widget","version":"1.0"}`, nil), 0, ""},
		{"version conflict", good, 0, "2.0"},
		{"bad json", archive(t, `{`, nil), 0, ""},
		{"truncated", good, int64(len(good)) + 1, ""},
		{"too large", good, ArchiveLimit + 1, ""},
		{"zip traversal", archive(t, `{"name":"acme/widget","version":"1.0"}`, map[string]string{"../escape": "x"}), 0, ""},
		{"not zip", []byte("bad"), 3, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size := tc.size
			if size == 0 {
				size = int64(len(tc.data))
			}
			if _, err := ParseArchive(
				context.Background(),
				bytes.NewReader(tc.data),
				size,
				tc.override,
			); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
	missing := archive(t, `{"name":"acme/widget"}`, nil)
	p, err = ParseArchive(
		context.Background(),
		bytes.NewReader(missing),
		int64(len(missing)),
		"1.0.0",
	)
	if err != nil || p.Version != "1.0.0" {
		t.Fatalf("matrix version %+v: %v", p, err)
	}
	if _, err := ParseArchive(context.Background(), strings.NewReader(""), 0, ""); err == nil {
		t.Fatal("empty accepted")
	}
}

func TestUnitHostedPathAndGrammar(t *testing.T) {
	for _, path := range []string{"acme/widget/1.0.zip", "acme/widget/release-1.0.zip"} {
		if !HostedPath(path, "acme/widget") {
			t.Errorf("rejected %s", path)
		}
	}
	for _, path := range []string{"other/widget/1.0.zip", "acme/widget/nested/1.0.zip", "acme/widget/1.0.tar.gz"} {
		if HostedPath(path, "acme/widget") {
			t.Errorf("accepted %s", path)
		}
	}
	for _, name := range []string{"Acme/widget", "../widget", "acme//widget", "acme/widget/extra"} {
		if ValidName(name) {
			t.Errorf("accepted %s", name)
		}
	}
	for _, version := range []string{"", "../1.0", "a/b", "a~b"} {
		if ValidVersion(version) {
			t.Errorf("accepted %s", version)
		}
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestUnitArchiveAdversarialInputs(t *testing.T) {
	good := archive(t, `{"name":"acme/widget","version":"1.0.0"}`, nil)
	for _, tc := range []struct {
		name     string
		data     []byte
		override string
	}{
		{"duplicate metadata", archive(t, `{"name":"acme/widget","version":"1.0.0"}`, map[string]string{"one/composer.json": `{"name":"acme/widget","version":"1.0.0"}`}), ""},
		{"bad version type", archive(t, `{"name":"acme/widget","version":123}`, nil), ""},
		{"bad version grammar", archive(t, `{"name":"acme/widget","version":"a/b"}`, nil), ""},
		{"invalid override", good, "../1"},
		{"invalid zip path", archive(t, `{"name":"acme/widget","version":"1.0.0"}`, map[string]string{"/absolute": "x"}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseArchive(
				context.Background(),
				bytes.NewReader(tc.data),
				int64(len(tc.data)),
				tc.override,
			); err == nil {
				t.Fatal("accepted malformed archive")
			}
		})
	}
	if _, err := ParseArchive(
		context.Background(),
		failedReader{},
		int64(len(good)),
		"",
	); err == nil {
		t.Fatal("reader fault accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ParseArchive(ctx, bytes.NewReader(good), int64(len(good)), ""); err == nil {
		t.Fatal("canceled parse accepted")
	}
	makeEntries := func(entries map[string]string) []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for name, content := range entries {
			f, err := z.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(f, content)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	missing := makeEntries(map[string]string{"README.md": "no manifest"})
	if _, err := ParseArchive(
		context.Background(),
		bytes.NewReader(missing),
		int64(len(missing)),
		"",
	); err == nil {
		t.Fatal("archive without composer.json accepted")
	}
	prefixed := makeEntries(
		map[string]string{"widget/composer.json": `{"name":"acme/widget","version":"1.0.0"}`},
	)
	if pkg, err := ParseArchive(
		context.Background(),
		bytes.NewReader(prefixed),
		int64(len(prefixed)),
		"",
	); err != nil ||
		pkg.Name != "acme/widget" {
		t.Fatalf("prefixed manifest %+v: %v", pkg, err)
	}
	corrupt := append([]byte(nil), good...)
	if len(corrupt) > 80 {
		corrupt[55] ^= 0xff
	}
	if _, err := ParseArchive(
		context.Background(),
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
		"",
	); err == nil {
		t.Fatal("corrupt archive accepted")
	}
	if doc := (Package{Name: "acme/widget", Version: "1.0.0"}).Document(); doc["name"] != "acme/widget" {
		t.Fatal(doc)
	}
}

func TestUnitHandlerClassAndCachedAdmission(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "php", Class: repo.Local, PackageType: repo.Composer})
	for _, w := range []format.Write{{Path: "a.txt", Cached: true, Op: format.OpDeploy}, {Path: "a.zip.sha1", Op: format.OpChecksum}} {
		if _, err := h.Admit(context.Background(), format.Env{}, c, w); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Admit(
		context.Background(),
		format.Env{},
		c,
		format.Write{Path: "a.txt", Op: format.OpDeploy},
	); err == nil {
		t.Fatal("non-zip admitted")
	}
	c.Class = repo.Remote
	if _, err := h.Admit(
		context.Background(),
		format.Env{},
		c,
		format.Write{Path: "a.zip", Op: format.OpDeploy},
	); err == nil {
		t.Fatal("remote write admitted")
	}
	c.Class = repo.Local
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Admit(
		context.Background(),
		format.Env{Blobs: blobs},
		c,
		format.Write{
			Path:      "acme/widget/1.0.0.zip",
			Op:        format.OpDeploy,
			Checksums: types.Checksums{SHA256: strings.Repeat("0", 64)},
		},
	); err == nil {
		t.Fatal("publication without a matching inspected archive admitted")
	}
}
