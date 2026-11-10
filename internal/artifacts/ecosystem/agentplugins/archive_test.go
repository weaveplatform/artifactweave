package agentplugins

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func archive(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range entries {
		f, err := w.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitReadArchiveAndPath(t *testing.T) {
	data := archive(
		t,
		[2]string{
			".claude-plugin/plugin.json",
			`{"name":"wrong","version":"999.0.0","description":"reader"}`,
		},
		[2]string{".cursor-plugin/plugin.json", `{"name":"reader"}`},
		[2]string{"scripts/run.sh", "echo hi"},
	)
	m, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil || strings.Join(m.Harnesses, ",") != "claude,cursor" ||
		!bytes.Contains(m.Manifests["claude"], []byte(`"wrong"`)) {
		t.Fatalf("metadata %+v, %v", m, err)
	}
	for _, p := range []string{"foo/1.2.3/foo-1.2.3.zip", "a-b/1.2.3-beta.1/a-b-1.2.3-beta.1.zip"} {
		if n, v, ok := ParsePath(p); !ok || Path(n, v) != p {
			t.Fatalf("bad path %s", p)
		}
	}
	for _, p := range []string{"foo/1/foo-1.zip", "foo/1.2.3/evil-1.2.3.zip", "../foo.zip"} {
		if _, _, ok := ParsePath(p); ok {
			t.Fatalf("accepted path %s", p)
		}
	}
}

func TestUnitReadArchiveRejectsInvalid(t *testing.T) {
	manifest := [2]string{".claude-plugin/plugin.json", `{"name":"reader"}`}
	for _, name := range []string{"../bad", "/abs", "a/../b", "a//b", `a\b`, "./a"} {
		data := archive(t, manifest, [2]string{name, "bad"})
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
		{{".claude-plugin/plugin.json", `{"description":"missing name"}`}},
		{{"file.txt", "no manifest"}},
	} {
		data := archive(t, entries...)
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
	data := archive(t, manifest)
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
		t.Fatalf("accepted truncated ZIP: %v", err)
	}
	root := archive(t, [2]string{"plugin.json", `{"name":"future"}`})
	m, err := ReadArchive(bytes.NewReader(root), int64(len(root)))
	if err != nil || !m.Root || len(m.Harnesses) != 0 {
		t.Fatalf("root: %+v %v", m, err)
	}
}

func TestUnitReadArchiveLimitsAndIntegrity(t *testing.T) {
	for _, body := range []string{"{", "null", `{"name":42}`, `{"name":""}`} {
		data := archive(t, [2]string{".claude-plugin/plugin.json", body})
		if _, err := ReadArchive(
			bytes.NewReader(data),
			int64(len(data)),
		); !errors.Is(
			err,
			ErrArchive,
		) {
			t.Fatalf("accepted manifest %q: %v", body, err)
		}
	}
	for _, entries := range [][][2]string{
		{{".claude-plugin/plugin.json", `{"name":"x","padding":"` + strings.Repeat("a", MaxManifestBytes) + `"}`}},
		{{".claude-plugin/plugin.json", `{"name":"x"}`}, {"other.txt", strings.Repeat("a", MaxArchiveBytes+1)}},
	} {
		data := archive(t, entries...)
		if _, err := ReadArchive(
			bytes.NewReader(data),
			int64(len(data)),
		); !errors.Is(
			err,
			ErrArchive,
		) {
			t.Fatalf("accepted oversized entry: %v", err)
		}
	}
	many := make([][2]string, MaxFiles+1)
	for i := range many {
		many[i] = [2]string{strings.Repeat("x", i+1), "x"}
	}
	data := archive(t, many...)
	if _, err := ReadArchive(bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrArchive) {
		t.Fatalf("accepted too many files: %v", err)
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.CreateHeader(&zip.FileHeader{Name: ".claude-plugin/plugin.json", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(`{"name":"crc"}`)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := b.Bytes()
	i := bytes.Index(corrupt, []byte(`{"name":"crc"}`))
	if i < 0 {
		t.Fatal("stored member missing")
	}
	corrupt[i] = 'X'
	if _, err := ReadArchive(
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted corrupted member: %v", err)
	}
}
