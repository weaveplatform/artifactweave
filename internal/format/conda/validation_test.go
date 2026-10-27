package conda

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func testArchive(t *testing.T, entries ...struct{ name, body string }) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for _, e := range entries {
		if err := w.WriteHeader(
			&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnitCondaRejectsArchiveMetadataAndPaths(t *testing.T) {
	id, _ := ParsePackagePath("noarch/demo-1.0-0.tar.bz2")
	valid := `{"name":"demo","version":"1.0","build":"0","build_number":0,"noarch":"generic"}`
	for _, tc := range []struct{ name, entry, content string }{
		{"missing index", "info/about.json", `{}`},
		{"traversal entry", "../info/index.json", valid},
		{"wrong identity", "info/index.json", strings.Replace(valid, `"demo"`, `"other"`, 1)},
		{"wrong subdir", "info/index.json", strings.Replace(valid, `"noarch":"generic"`, `"subdir":"linux-64"`, 1)},
		{"bad dependency", "info/index.json", strings.Replace(valid, `"build_number":0`, `"build_number":0,"depends":["a\ninvalid"]`, 1)},
		{"invalid json", "info/index.json", "{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := testArchive(t, struct{ name, body string }{tc.entry, tc.content})
			_, err := readTar(t.Context(), bytes.NewReader(archive), id)
			if !errors.Is(err, ErrInvalidPackage) {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
		})
	}
	archive := testArchive(t, struct{ name, body string }{"info/index.json", valid},
		struct{ name, body string }{"info/index.json", valid})
	if _, err := readTar(
		t.Context(),
		bytes.NewReader(archive),
		id,
	); !errors.Is(
		err,
		ErrInvalidPackage,
	) {
		t.Fatalf("duplicate index accepted: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readTar(
		canceled,
		bytes.NewReader(archive),
		id,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancel did not stop archive parsing: %v", err)
	}
	if _, err := ReadPackage(
		t.Context(),
		bytes.NewReader(archive),
		0,
		id,
	); !errors.Is(
		err,
		ErrInvalidPackage,
	) {
		t.Fatalf("zero-sized package accepted: %v", err)
	}
	if _, err := ReadPackage(
		t.Context(),
		bytes.NewReader([]byte("bad zip")),
		7,
		PackagePath{
			Path:      "noarch/demo-1.0-0.conda",
			Extension: ".conda",
		},
	); !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("invalid modern archive accepted: %v", err)
	}
	for _, p := range []string{"noarch/a-1-0.conda", "noarch/a-1-0.tar.bz2"} {
		if IsMetadata(p) {
			t.Fatalf("package classified as metadata: %s", p)
		}
	}
	if !IsMetadata("noarch/repodata.json") ||
		IndexPath("channel", "noarch", "repodata.json") != "channel/noarch/repodata.json" {
		t.Fatal("channel index path not recognized")
	}
}

func TestUnitCondaRepodataRejectsUnsafeRemoteClaims(t *testing.T) {
	id, _ := ParseMetadataPath("noarch/repodata.json")
	base := EmptyRepodata("noarch")
	base.Packages["demo-1.0-0.tar.bz2"] = Record{
		Name: "demo", Version: "1.0", Build: "0", Size: 12,
		SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("example"))),
	}
	marshal := func(t *testing.T, r Repodata) []byte {
		t.Helper()
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, tc := range []struct {
		name string
		edit func(*Repodata)
	}{
		{"wrong platform", func(r *Repodata) { r.Info.Subdir = "linux-64" }},
		{"unsupported version", func(r *Repodata) { r.RepodataVersion = 2 }},
		{"removed traversal", func(r *Repodata) { r.Removed = []string{"../evil-1-0.conda"} }},
		{"wrong package identity", func(r *Repodata) {
			x := r.Packages["demo-1.0-0.tar.bz2"]
			x.Name = "other"
			r.Packages["demo-1.0-0.tar.bz2"] = x
		}},
		{"missing checksum", func(r *Repodata) {
			x := r.Packages["demo-1.0-0.tar.bz2"]
			x.SHA256 = ""
			r.Packages["demo-1.0-0.tar.bz2"] = x
		}},
		{"empty dependency", func(r *Repodata) {
			x := r.Packages["demo-1.0-0.tar.bz2"]
			x.Depends = []string{""}
			r.Packages["demo-1.0-0.tar.bz2"] = x
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := base
			idx.Packages = map[string]Record{
				"demo-1.0-0.tar.bz2": base.Packages["demo-1.0-0.tar.bz2"],
			}
			tc.edit(&idx)
			if _, err := DecodeRepodata(marshal(t, idx), id); !errors.Is(err, ErrInvalidRepodata) {
				t.Fatalf("unsafe remote metadata accepted: %v", err)
			}
			if tc.name == "wrong package identity" || tc.name == "missing checksum" ||
				tc.name == "empty dependency" {
				if _, err := EncodeRepodata(
					idx,
					"",
					"noarch",
				); !errors.Is(
					err,
					ErrInvalidRepodata,
				) {
					t.Fatalf("unsafe local metadata encoded: %v", err)
				}
			}
		})
	}
	for _, body := range [][]byte{nil, []byte("{"), []byte(`{"info":{"subdir":"linux-64"}}`)} {
		if _, err := DecodeRepodata(body, id); !errors.Is(err, ErrInvalidRepodata) {
			t.Fatalf("invalid metadata accepted: %q %v", body, err)
		}
	}
	if _, err := DecodeRepodata(
		[]byte("invalid zstd"),
		MetadataPath{Filename: "repodata.json.zst"},
	); !errors.Is(
		err,
		ErrInvalidRepodata,
	) {
		t.Fatalf("invalid zstd accepted: %v", err)
	}
	if _, err := DecodeRepodata(
		[]byte("data"),
		MetadataPath{Filename: "notices.json"},
	); !errors.Is(
		err,
		ErrInvalidRepodata,
	) {
		t.Fatalf("non-repodata accepted: %v", err)
	}
	if _, err := EncodeRepodata(base, "", "linux-64"); !errors.Is(err, ErrInvalidRepodata) {
		t.Fatalf("cross-platform index encoded: %v", err)
	}
	encoded, err := EncodeRepodata(base, "", "noarch")
	if err != nil {
		t.Fatal(err)
	}
	zst := encoded["repodata.json.zst"]
	z, err := zstd.NewReader(bytes.NewReader(zst))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	plain, err := io.ReadAll(z)
	if err != nil || !bytes.Equal(plain, encoded["repodata.json"]) {
		t.Fatalf("compressed index differs from plain: %v", err)
	}
}

func TestUnitCondaRealModernArchiveRejectsTruncation(t *testing.T) {
	data, err := os.ReadFile(
		filepath.Join("testdata", "ca-certificates-2026.7.22-hbd8a1cb_0.conda"),
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParsePackagePath("noarch/ca-certificates-2026.7.22-hbd8a1cb_0.conda")
	for _, size := range []int{1, len(data) / 2} {
		_, err := ReadPackage(t.Context(), bytes.NewReader(data[:size]), int64(size), id)
		if !errors.Is(err, ErrInvalidPackage) {
			t.Fatalf("truncated .conda accepted at size %d: %v", size, err)
		}
	}
}

func TestUnitCondaModernArchiveMemberIntegrity(t *testing.T) {
	id, _ := ParsePackagePath("noarch/demo-1.0-0.conda")
	index := `{"name":"demo","version":"1.0","build":"0","build_number":0,"noarch":"generic"}`
	compress := func(body []byte) []byte {
		t.Helper()
		var out bytes.Buffer
		z, err := zstd.NewWriter(&out)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := z.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	info := compress(testArchive(t, struct{ name, body string }{"info/index.json", index}))
	pkg := compress(testArchive(t, struct{ name, body string }{"bin/demo", "payload"}))
	base := map[string][]byte{
		"metadata.json":  []byte(`{"conda_pkg_format_version":2}`),
		"info-a.tar.zst": info,
		"pkg-a.tar.zst":  pkg,
	}
	zipBody := func(members map[string][]byte) []byte {
		t.Helper()
		var out bytes.Buffer
		w := zip.NewWriter(&out)
		for name, data := range members {
			member, err := w.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := member.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	for _, tc := range []struct {
		name string
		edit func(map[string][]byte)
		bad  bool
	}{
		{"valid modern archive", func(map[string][]byte) {}, false},
		{"missing payload", func(m map[string][]byte) { delete(m, "pkg-a.tar.zst") }, true},
		{"duplicate info", func(m map[string][]byte) { m["info-b.tar.zst"] = info }, true},
		{"arbitrary member", func(m map[string][]byte) { m["script.sh"] = []byte("echo bad") }, true},
		{"traversal member", func(m map[string][]byte) { m["../evil"] = []byte("bad") }, true},
		{"wrong format version", func(m map[string][]byte) { m["metadata.json"] = []byte(`{"conda_pkg_format_version":3}`) }, true},
		{"corrupt info zstd", func(m map[string][]byte) { m["info-a.tar.zst"] = []byte("bad") }, true},
		{"corrupt payload tar", func(m map[string][]byte) { m["pkg-a.tar.zst"] = compress([]byte("not tar")) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := make(map[string][]byte, len(base))
			for name, data := range base {
				members[name] = data
			}
			tc.edit(members)
			body := zipBody(members)
			got, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)), id)
			if tc.bad && !errors.Is(err, ErrInvalidPackage) {
				t.Fatalf("malformed member accepted: %+v %v", got, err)
			}
			if !tc.bad && (err != nil || got.Name != "demo") {
				t.Fatalf("valid modern archive rejected: %+v %v", got, err)
			}
		})
	}
}

func TestUnitCondaRepodataModernAndLegacyRecordsRemainSeparate(t *testing.T) {
	legacy := Record{
		Name: "demo", Version: "1.0", Build: "0", Size: 1,
		SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("legacy"))),
	}
	modern := legacy
	modern.Size = 2
	modern.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("modern")))
	index := EmptyRepodata("linux-64")
	index.Packages["demo-1.0-0.tar.bz2"] = legacy
	index.PackagesConda["demo-1.0-0.conda"] = modern
	index.Removed = []string{"obsolete-0.1-0.tar.bz2"}
	encoded, err := EncodeRepodata(index, "science", "linux-64")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"repodata.json", "repodata.json.zst", "current_repodata.json.zst"} {
		path, ok := ParseMetadataPath("science/linux-64/" + file)
		if !ok {
			t.Fatalf("generated metadata path %s refused", file)
		}
		decoded, err := DecodeRepodata(encoded[file], path)
		if err != nil || decoded.Packages["demo-1.0-0.tar.bz2"].SHA256 != legacy.SHA256 ||
			decoded.PackagesConda["demo-1.0-0.conda"].SHA256 != modern.SHA256 ||
			len(decoded.Removed) != 1 {
			t.Fatalf("archive kinds or removals changed in %s: %+v %v", file, decoded, err)
		}
	}
	// This static bzip2 channel index was produced from the canonical JSON
	// below. The codec reads remote bz2 metadata even though hosted channels
	// negotiate JSON or Zstandard.
	const compressed = "QlpoOTFBWSZTWVkby50AACwbgBAFIBAACr/r3wogAFCgAGgAAGlGTaJ6CZBozU86l3eWUgBr0pUN0QWlIJjA+E25qYtfk/FMaYGzx9W0mBEGXZhVpq4PajAwp6sii+3J2Av4u5IpwoSCyN5c6A=="
	data, err := base64.StdEncoding.DecodeString(compressed)
	if err != nil {
		t.Fatal(err)
	}
	bz2Path, _ := ParseMetadataPath("noarch/repodata.json.bz2")
	if _, err := DecodeRepodata(data, bz2Path); err != nil {
		t.Fatalf("valid remote bzip2 repodata rejected: %v", err)
	}
	if _, err := DecodeRepodata([]byte("not bz2"), bz2Path); !errors.Is(err, ErrInvalidRepodata) {
		t.Fatalf("corrupt remote bzip2 metadata accepted: %v", err)
	}
}

func TestUnitCondaArchiveRejectsUnsafeTarLinksAndLargeIndex(t *testing.T) {
	id, _ := ParsePackagePath("noarch/demo-1.0-0.tar.bz2")
	for _, tc := range []struct {
		name, target string
	}{
		{"parent traversal", "../../private/key"},
		{"absolute target", "/etc/passwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			w := tar.NewWriter(&body)
			if err := w.WriteHeader(
				&tar.Header{
					Name:     "bin/tool",
					Typeflag: tar.TypeSymlink,
					Linkname: tc.target,
					Mode:     0o777,
				},
			); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := readTar(
				t.Context(),
				bytes.NewReader(body.Bytes()),
				id,
			); !errors.Is(
				err,
				ErrInvalidPackage,
			) {
				t.Fatalf("unsafe TAR link accepted: %v", err)
			}
		})
	}
	large := testArchive(
		t,
		struct{ name, body string }{"info/index.json", strings.Repeat("x", int(maxIndexJSON)+1)},
	)
	if _, err := readTar(
		t.Context(),
		bytes.NewReader(large),
		id,
	); !errors.Is(
		err,
		ErrInvalidPackage,
	) {
		t.Fatalf("oversized package identity accepted: %v", err)
	}
	var broken bytes.Buffer
	w := tar.NewWriter(&broken)
	if err := w.WriteHeader(
		&tar.Header{Name: "info/index.json", Mode: 0o644, Size: 100},
	); err != nil {
		t.Fatal(err)
	}
	// A truncated network body must fail rather than publishing an index.
	if _, err := readTar(
		t.Context(),
		bytes.NewReader(broken.Bytes()),
		id,
	); !errors.Is(
		err,
		ErrInvalidPackage,
	) {
		t.Fatalf("truncated TAR index accepted: %v", err)
	}
}

func TestUnitCondaRepodataEnforcesBoundedInput(t *testing.T) {
	id, _ := ParseMetadataPath("noarch/repodata.json")
	if _, err := DecodeRepodata(
		bytes.Repeat([]byte("x"), maxRepodataBytes+1),
		id,
	); !errors.Is(
		err,
		ErrInvalidRepodata,
	) {
		t.Fatalf("oversized remote index accepted: %v", err)
	}
	index := EmptyRepodata("noarch")
	index.PackagesConda["demo-1.0-0.conda"] = Record{
		Name:    "demo",
		Version: "1.0",
		Build:   "0",
		Size:    1,
		SHA256:  "not-hex",
	}
	if _, err := EncodeRepodata(index, "", "noarch"); !errors.Is(err, ErrInvalidRepodata) {
		t.Fatalf("modern package without a valid digest indexed: %v", err)
	}
	empty, err := DecodeRepodata([]byte(`{}`), id)
	if err != nil || empty.Packages == nil || empty.PackagesConda == nil || empty.Removed == nil ||
		empty.Info.Subdir != "noarch" {
		t.Fatalf("legacy sparse index not normalized: %+v %v", empty, err)
	}
	if _, err := EncodeRepodata(empty, "", "noarch"); err != nil {
		t.Fatalf("normalized empty channel failed encoding: %v", err)
	}
}

type brokenCondaSeeker struct{}

func (brokenCondaSeeker) Read([]byte) (int, error)       { return 0, io.ErrUnexpectedEOF }
func (brokenCondaSeeker) Seek(int64, int) (int64, error) { return 0, io.ErrUnexpectedEOF }

func TestUnitCondaPackageIndexRejectsMalformedFields(t *testing.T) {
	id, _ := ParsePackagePath("noarch/demo-1.0-0.tar.bz2")
	for _, data := range [][]byte{
		nil,
		[]byte(`{"name":"demo","version":"1.0","build":"0","license":"bad\nlicense"}`),
		[]byte(`{"name":"demo","version":"1.0","build":"0","depends":["bad\u0007dependency"]}`),
	} {
		if _, err := parsePackageIndex(data, id); !errors.Is(err, ErrInvalidPackage) {
			t.Fatalf("invalid package identity accepted: %q %v", data, err)
		}
	}
	if _, err := ReadPackage(
		t.Context(),
		brokenCondaSeeker{},
		1,
		id,
	); !errors.Is(
		err,
		io.ErrUnexpectedEOF,
	) {
		t.Fatalf("package reader seek failure was ignored: %v", err)
	}
	id.Extension = ".zip"
	if _, err := ReadPackage(
		t.Context(),
		bytes.NewReader([]byte("payload")),
		7,
		id,
	); !errors.Is(
		err,
		ErrInvalidPackage,
	) {
		t.Fatalf("unsupported package extension accepted: %v", err)
	}
	r := &lockedReaderAt{body: brokenCondaSeeker{}}
	if _, err := r.ReadAt(make([]byte, 1), 0); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ZIP random access ignored seek failure: %v", err)
	}
}
