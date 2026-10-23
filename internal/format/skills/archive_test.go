package skills

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func archive(t *testing.T, entries []struct{ name, body string }) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestArchiveMetadataAndSafePaths(t *testing.T) {
	valid := archive(t, []struct{ name, body string }{
		{"nested/", ""},
		{
			"nested/SKILL.md",
			"---\nname: top\nmetadata:\n  skills:\n    author: Ada\n    tags: [one, two]\n---\nbody",
		},
		{"nested/action.js", "console.log('ok')"},
	})
	meta, err := ReadArchive(bytes.NewReader(valid), int64(len(valid)))
	if err != nil || meta.Name != "top" || meta.Author != "Ada" || len(meta.Tags) != 2 {
		t.Fatalf("metadata %+v, %v", meta, err)
	}
	for _, bad := range []string{"../escape", "/absolute", "a/../b", "a//b", "a\\b", "./prefix"} {
		t.Run(bad, func(t *testing.T) {
			data := archive(t, []struct{ name, body string }{{bad, "x"}})
			if _, err := ReadArchive(
				bytes.NewReader(data),
				int64(len(data)),
			); !errors.Is(
				err,
				ErrArchive,
			) {
				t.Fatalf("accepted unsafe name %q: %v", bad, err)
			}
		})
	}
	duplicate := archive(t, []struct{ name, body string }{{"x", "one"}, {"x", "two"}})
	if _, err := ReadArchive(
		bytes.NewReader(duplicate),
		int64(len(duplicate)),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted duplicate entry: %v", err)
	}
}

func TestArchiveRejectsTruncatedAndOversizedMetadata(t *testing.T) {
	data := archive(t, []struct{ name, body string }{{"SKILL.md", "---\nname: good\n---\n"}})
	if _, err := ReadArchive(
		bytes.NewReader(data[:len(data)-8]),
		int64(len(data)-8),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted truncated ZIP: %v", err)
	}
	if _, err := ReadArchive(
		bytes.NewReader(data),
		MaxArchiveBytes+1,
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted oversized declared ZIP: %v", err)
	}
	oversized := archive(
		t,
		[]struct{ name, body string }{
			{"SKILL.md", string(bytes.Repeat([]byte("x"), MaxSkillBytes+1))},
		},
	)
	if _, err := ReadArchive(
		bytes.NewReader(oversized),
		int64(len(oversized)),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted oversized SKILL.md: %v", err)
	}
}

func TestArchiveAllowsMissingManifestButRejectsCorruptEntries(t *testing.T) {
	plain := archive(t, []struct{ name, body string }{{"src/run.js", "run()"}})
	if got, err := ReadArchive(bytes.NewReader(plain), int64(len(plain))); err != nil ||
		got.Name != "" || got.Fingerprint != "" {
		t.Fatalf("manifest-free bundle: %+v %v", got, err)
	}
	if _, err := ReadArchive(
		bytes.NewReader(plain),
		int64(len(plain)+1),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted length mismatch: %v", err)
	}
	unmarked := archive(t, []struct{ name, body string }{{"SKILL.md", "# Markdown only\n"}})
	if got, err := ReadArchive(bytes.NewReader(unmarked), int64(len(unmarked))); err != nil ||
		got.Name != "" {
		t.Fatalf("unmarked SKILL.md: %+v %v", got, err)
	}
	empty := archive(t, nil)
	if _, err := ReadArchive(
		bytes.NewReader(empty),
		int64(len(empty)),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted empty ZIP: %v", err)
	}
	long := archive(t, []struct{ name, body string }{{
		"SKILL.md",
		"---\nsummary: " + strings.Repeat("x", 4097) + "\n---\n",
	}})
	if _, err := ReadArchive(bytes.NewReader(long), int64(len(long))); !errors.Is(err, ErrArchive) {
		t.Fatalf("accepted excessive metadata field: %v", err)
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("../escape"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArchive(
		bytes.NewReader(b.Bytes()),
		int64(b.Len()),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted symlink: %v", err)
	}
	b.Reset()
	zw = zip.NewWriter(&b)
	h = &zip.FileHeader{Name: "payload.bin", Method: zip.Store}
	w, err = zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("known payload"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(b.Bytes(), []byte("known payload"), []byte("wrong payload"), 1)
	if _, err := ReadArchive(
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
	); !errors.Is(
		err,
		ErrArchive,
	) {
		t.Fatalf("accepted a corrupt ZIP entry: %v", err)
	}
}

func TestArchiveSelectsClosestManifestAndNestedMetadata(t *testing.T) {
	data := archive(t, []struct{ name, body string }{
		{"a/SKILL.md", "---\nname: nested\n---\n"},
		{
			"SKILL.md",
			"---\nname: root\nmetadata:\n  clawdbot:\n    author: Ada\n  clawdis:\n    displayName: Root\n---\n",
		},
	})
	meta, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil || meta.Name != "root" || meta.Author != "Ada" || meta.DisplayName != "Root" {
		t.Fatalf("selected metadata %+v %v", meta, err)
	}
}

func TestArchiveReadsSupportedMetadataBlocksWithinBounds(t *testing.T) {
	data := archive(t, []struct{ name, body string }{{"SKILL.md", `---
name: root
metadata:
  skills:
    name: nested
    description: Automated checks
    version: 9.0.0
    author: Ada
    tags: [ci, code]
    displayName: Checks
    summary: Runs checks
    changelog: New rules
---
`}})
	meta, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil || meta.Name != "nested" || meta.Description != "Automated checks" ||
		meta.Author != "Ada" || meta.DisplayName != "Checks" || meta.Summary != "Runs checks" ||
		meta.Changelog != "New rules" || len(meta.Tags) != 2 {
		t.Fatalf("nested metadata %+v %v", meta, err)
	}
	for name, content := range map[string]string{
		"too many tags": "---\ntags: [" + strings.Repeat("a,", 65) + "z]\n---\n",
		"long tag":      "---\ntags: [" + strings.Repeat("x", 257) + "]\n---\n",
		"unterminated":  "---\nname: unfinished\n",
	} {
		t.Run(name, func(t *testing.T) {
			bad := archive(t, []struct{ name, body string }{{"SKILL.md", content}})
			if _, err := ReadArchive(
				bytes.NewReader(bad),
				int64(len(bad)),
			); !errors.Is(
				err,
				ErrArchive,
			) {
				t.Fatalf("accepted invalid metadata: %v", err)
			}
		})
	}
}

func TestCanonicalPathAndVersion(t *testing.T) {
	for _, v := range []string{"1.2.3", "1.2.3-rc.1", "1.2.3+build.7"} {
		if !ValidVersion(v) {
			t.Fatalf("refused version %q", v)
		}
		if s, got, ok := ParsePath(Path("skill-one", v)); !ok || s != "skill-one" || got != v {
			t.Fatalf("path round trip %q %q %t", s, got, ok)
		}
	}
	for _, v := range []string{"v1.2.3", "01.2.3", "../2.0", "1.2", "1.2.3."} {
		if ValidVersion(v) {
			t.Fatalf("accepted invalid version %q", v)
		}
	}
}
