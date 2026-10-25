package bazelmodules

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitPathAndManifestIdentity(t *testing.T) {
	for _, path := range []string{
		"modules/hello/1.0.0/MODULE.bazel",
		"modules/hello/1.0.0/hello-1.0.0.tar.gz",
		"modules/hello/1.0.0/hello.zip",
		"modules/hello/1.0.0/hello.tgz",
		"modules/hello/1.0.0/hello.tar",
	} {
		if _, ok := ParseFile(path); !ok {
			t.Errorf("valid path refused: %s", path)
		}
	}
	for _, path := range []string{
		"modules/hello/1.0.0/source.json", "modules/hello/../MODULE.bazel",
		"modules/hello/1.0.0/bad%2Fname.zip", "modules/hello/1.0.0/README",
	} {
		if _, ok := ParseFile(path); ok {
			t.Errorf("unsafe path admitted: %s", path)
		}
	}
	good := []byte(
		"module(\n    name = \"hello\",\n    version = \"1.0.0\",\n    compatibility_level = 1,\n)\n",
	)
	name, version, err := ParseModule(good)
	if err != nil || name != "hello" || version != "1.0.0" {
		t.Fatalf("valid MODULE.bazel: %q %q %v", name, version, err)
	}
	for _, body := range [][]byte{
		[]byte("module(name=\"other\")"),
		[]byte("module(name=\"hello\", version=\"1.0.0\", name=\"evil\")"),
		[]byte("module(name=dynamic(), version=\"1.0.0\")"),
		[]byte("module(name=\"hello\", version=\"1.0.0\")\nmodule(name=\"evil\", version=\"1\")"),
		[]byte("module(name=\"hello\", version=\"../escape\")"),
		[]byte("module(name=\"hello\", version=\"1.0.0\")\x00"),
		bytes.Repeat([]byte("x"), maxModuleBytes+1),
	} {
		if _, _, err := ParseModule(body); err == nil {
			t.Errorf("invalid MODULE.bazel admitted: %q", body[:min(len(body), 100)])
		}
	}
}

func TestUnitIntegrityEncoding(t *testing.T) {
	digest := sha256.Sum256([]byte("archive"))
	integrity, err := Integrity(hex.EncodeToString(digest[:]))
	if err != nil || !strings.HasPrefix(integrity, "sha256-") {
		t.Fatal(integrity, err)
	}
	if _, err := Integrity("bad"); err == nil {
		t.Fatal("invalid digest admitted")
	}
}

func TestUnitArchiveStripPrefixUsesTheActualArchiveLayout(t *testing.T) {
	makeArchive := func(names ...string) []byte {
		var out bytes.Buffer
		gz := gzip.NewWriter(&out)
		tw := tar.NewWriter(gz)
		for _, name := range names {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"hello-1.0.0/BUILD.bazel", "hello-1.0.0/message.txt"}, "hello-1.0.0"},
		{[]string{"BUILD.bazel", "message.txt"}, ""},
		{[]string{"a/BUILD.bazel", "b/message.txt"}, ""},
	} {
		body := makeArchive(tc.names...)
		got, err := ArchiveStripPrefix(bytes.NewReader(body), int64(len(body)), "hello.tar.gz")
		if err != nil || got != tc.want {
			t.Fatalf("%v: %q %v", tc.names, got, err)
		}
	}
	if _, err := ArchiveStripPrefix(bytes.NewReader([]byte("bad")), 3, "bad.bin"); err == nil {
		t.Fatal("nonarchive accepted")
	}
	unsafe := makeArchive("../escape/BUILD.bazel")
	if _, err := ArchiveStripPrefix(
		bytes.NewReader(unsafe),
		int64(len(unsafe)),
		"unsafe.tar.gz",
	); err == nil {
		t.Fatal("unsafe archive path accepted")
	}
}

func TestUnitAdmissionRejectsInvalidPaths(t *testing.T) {
	h := Handler{}
	c := repo.Config{Key: "bazel", PackageType: repo.BazelModules, Class: repo.Local}
	for _, path := range []string{"modules/hello/1.0.0/source.json", "modules/hello/1.0.0/README", "modules/hello/../MODULE.bazel"} {
		if _, err := h.Admit(
			context.Background(),
			format.Env{},
			c,
			format.Write{Path: path, Op: format.OpDeploy},
		); err == nil {
			t.Errorf("invalid path admitted: %s", path)
		}
	}
	if _, err := h.Admit(
		context.Background(),
		format.Env{},
		c,
		format.Write{
			Path:      "modules/hello/1.0.0/MODULE.bazel",
			Op:        format.OpDeploy,
			Checksums: types.Checksums{SHA256: "missing"},
		},
	); err == nil {
		t.Fatal("uninspected module admitted")
	}
	if err := h.GuardWrite(
		context.Background(),
		format.Env{},
		c,
		format.Write{Path: "modules/hello/1.0.0/hello.tar.gz", Cached: true},
	); err != nil {
		t.Fatal("cached archive should preserve its existing version: ", err)
	}
}
