package agentpackages

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func fixtureZIP(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, e := range entries {
		f, err := z.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitArchiveAndCanonicalPath(t *testing.T) {
	data := fixtureZIP(t,
		[2]string{"apm.yml", "name: my-pkg\nversion: 1.2.3\ndescription: example\n"},
		[2]string{".apm/skills/help/SKILL.md", "---\nname: help\n---\n"},
	)
	meta, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil || meta.Name != "my-pkg" || meta.Version != "1.2.3" ||
		meta.Description != "example" {
		t.Fatalf("metadata: %+v, %v", meta, err)
	}
	for _, p := range []string{"acme/my-pkg/my-pkg-1.2.3.zip", "a-b/pkg/pkg-1.2.3-beta.1.zip"} {
		o, n, v, ok := ParsePath(p)
		if !ok || Path(o, n, v) != p {
			t.Fatalf("invalid path %s", p)
		}
	}
	for _, p := range []string{
		"acme/my-pkg/1.2.3/my-pkg-1.2.3.zip", "acme/my-pkg/other-1.2.3.zip",
		"acme/my-pkg/my-pkg-v1.2.3.zip", "../pkg/pkg-1.2.3.zip",
	} {
		if _, _, _, ok := ParsePath(p); ok {
			t.Fatalf("accepted path %s", p)
		}
	}
	lock := fixtureZIP(t, [2]string{"apm.lock.yaml", "lockfile_version: '1'\ndependencies: []\n"})
	if _, err := ReadArchive(bytes.NewReader(lock), int64(len(lock))); err != nil {
		t.Fatalf("lockfile fallback: %v", err)
	}
	withDirectory := fixtureZIP(t, [2]string{"folder/", ""},
		[2]string{"apm.yml", "name: pkg\nversion: 1.0.0\n"})
	if _, err := ReadArchive(
		bytes.NewReader(withDirectory),
		int64(len(withDirectory)),
	); err != nil {
		t.Fatalf("directory entry: %v", err)
	}
}

func TestUnitArchiveRejectsUnsafeOrCorrupt(t *testing.T) {
	manifest := [2]string{"apm.yml", "name: my-pkg\nversion: 1.2.3\n"}
	for _, name := range []string{"../bad", "/abs", "a/../b", "a//b", `a\b`, "./a"} {
		data := fixtureZIP(t, manifest, [2]string{name, "bad"})
		if _, err := ReadArchive(
			bytes.NewReader(data),
			int64(len(data)),
		); !errors.Is(
			err,
			ErrArchive,
		) {
			t.Fatalf("accepted unsafe %q: %v", name, err)
		}
	}
	for _, entries := range [][][2]string{
		{{"x", "one"}, {"x", "two"}, manifest},
		{{"apm.yml", "name: package\nversion: no\n"}},
		{{"apm.yml", "version: 1.2.3\n"}},
		{{"file.txt", "no manifest"}},
		{{"apm.lock.yaml", "invalid: ["}},
	} {
		data := fixtureZIP(t, entries...)
		if _, err := ReadArchive(
			bytes.NewReader(data),
			int64(len(data)),
		); !errors.Is(
			err,
			ErrArchive,
		) {
			t.Fatalf("accepted malformed ZIP: %v", err)
		}
	}
	data := fixtureZIP(t, manifest)
	for _, size := range []int64{int64(len(data)) + 1, MaxArchiveBytes + 1} {
		if _, err := ReadArchive(bytes.NewReader(data), size); !errors.Is(err, ErrArchive) {
			t.Fatalf("accepted size %d: %v", size, err)
		}
	}
	if _, err := ReadArchive(
		bytes.NewReader(data[:len(data)-8]),
		int64(len(data)-8),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatal("accepted truncated archive")
	}
	oversized := fixtureZIP(t, manifest, [2]string{"other", strings.Repeat("x", MaxArchiveBytes+1)})
	if _, err := ReadArchive(
		bytes.NewReader(oversized),
		int64(len(oversized)),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatal("accepted excessive inflated data")
	}
	bigManifest := fixtureZIP(t, [2]string{"apm.yml", strings.Repeat("x", MaxManifestBytes+1)})
	if _, err := ReadArchive(
		bytes.NewReader(bigManifest),
		int64(len(bigManifest)),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatal("accepted oversized manifest")
	}
}
