package chef

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func archiveFixture(t *testing.T, entries []tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	for i := range entries {
		if err := tarWriter.WriteHeader(&entries[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(bodies[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnitReadArchiveValidatesWholeTarball(t *testing.T) {
	m := []byte(
		`{"name":"acme-widget","version":"1.2.3","dependencies":{"helper":">= 1.0.0"}}`,
	)
	entry := tar.Header{Name: "acme-widget/metadata.json", Mode: 0o644, Size: int64(len(m))}
	good := archiveFixture(t, []tar.Header{entry}, [][]byte{m})
	meta, err := ReadArchive(bytes.NewReader(good), int64(len(good)))
	if err != nil || meta.Name != "acme-widget" || meta.Version != "1.2.3" ||
		len(meta.Manifest.Dependencies) != 1 {
		t.Fatalf("archive: %+v %v", meta, err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"truncated", good[:len(good)-5]},
		{"trailing gzip", append(bytes.Clone(good), 1)},
		{"invalid gzip", []byte("bad")},
		{"traversal", archiveFixture(t, []tar.Header{entry, {Name: "../secret", Size: 1}}, [][]byte{m, []byte("x")})},
		{"different root", archiveFixture(t, []tar.Header{entry, {Name: "other/file", Size: 1}}, [][]byte{m, []byte("x")})},
		{"duplicate", archiveFixture(t, []tar.Header{entry, entry}, [][]byte{m, m})},
		{"link", archiveFixture(t, []tar.Header{entry, {Name: "acme-widget/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, [][]byte{m, nil})},
		{"missing manifest", archiveFixture(t, []tar.Header{{Name: "acme-widget/file", Size: 1}}, [][]byte{[]byte("x")})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadArchive(
				bytes.NewReader(tc.data),
				int64(len(tc.data)),
			); !errors.Is(
				err,
				ErrArchive,
			) {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
		})
	}
	for _, size := range []int64{-1, 0, 1, MaxArchiveBytes + 1} {
		if _, err := ReadArchive(bytes.NewReader(good), size); err == nil {
			t.Fatalf("accepted size %d", size)
		}
	}
	for _, manifest := range []string{`{`, `{"name":"wrong","version":"1.2.3"}`, `{"name":"acme-widget","version":"1.2.3","dependencies":{"helper":"garbage"}}`, `{"name":"acme-widget","version":"1.2.x"}`} {
		entry.Size = int64(len(manifest))
		body := archiveFixture(t, []tar.Header{entry}, [][]byte{[]byte(manifest)})
		if _, err := ReadArchive(bytes.NewReader(body), int64(len(body))); err == nil {
			t.Fatalf("bad manifest %q", manifest)
		}
	}
}

func TestUnitNamesPathsAndRequirements(t *testing.T) {
	for _, name := range []string{"widget", "acme-widget", "UPPER_widget", "a-b_c"} {
		if !ValidName(name) {
			t.Errorf("valid %s", name)
		}
	}
	for _, name := range []string{"", "../x", "a/b", "a.b"} {
		if ValidName(name) {
			t.Errorf("invalid %s", name)
		}
	}
	for _, v := range []string{"1.2", "1.2.3"} {
		n, got, ok := ParsePath(Path("widget", v))
		if !ok || n != "widget" || got != v {
			t.Fatal("roundtrip")
		}
	}
	for _, v := range []string{"1", "01.2.3", "1.2.3-rc.1", "1.2.3.4"} {
		if ValidVersion(v) {
			t.Fatal(v)
		}
	}
	for _, p := range []string{"x", "widget/other-1.0.0.tar.gz", "widget/widget-1.0.0.tar.gz/x"} {
		if _, _, ok := ParsePath(p); ok {
			t.Fatal(p)
		}
	}
	for _, m := range []Manifest{{Name: "widget", Version: "1.0.0", Dependencies: map[string]string{"widget": ">= 1.0.0"}}, {Name: "widget", Version: "1.0.0", Platforms: map[string]string{"ubuntu": "garbage"}}} {
		if ValidateManifest(m) {
			t.Fatal(m)
		}
	}
	if validEntry(nil, map[string]bool{}) {
		t.Fatal("nil entry")
	}
}

func TestUnitManifestRejectsAmbiguousJSON(t *testing.T) {
	for _, value := range []string{`null`, `[]`, `{"Name":"acme-widget"}`, `{"verſion":"1.0.0"}`, `{"dependencies":[{"Version_Requirement":"*"}]}`, `{"version":"1.0.0","verſion":"2.0.0"}`, `{"name":"acme-widget","Name":"acme-other"}`, `{"name":1}`, `{"dependencies":[{"name":"acme/helper","name":"acme/other"}]}`, `{"dependencies":[{"name":1}]}`, `{"dependencies":[null]}`, `{"x":`, `{"x"`, `{"x":1`, `{} {}`} {
		var m Manifest
		if err := json.Unmarshal([]byte(value), &m); err == nil {
			t.Fatalf("ambiguous manifest accepted %s", value)
		}
	}
	for _, value := range []string{`{"x"`, `{"x":`, `{"x":1`, `{} {}`} {
		if err := UniqueFields([]byte(value)); err == nil {
			t.Fatal(value)
		}
	}
}

func TestUnitArchiveDirectoriesPaddingAndOversizedManifest(t *testing.T) {
	m := []byte(`{"name":"acme-widget","version":"1.0.0"}`)
	entries := []tar.Header{
		{Name: "acme-widget/", Typeflag: tar.TypeDir},
		{Name: "acme-widget/metadata.json", Size: int64(len(m))},
	}
	good := archiveFixture(t, entries, [][]byte{nil, m})
	if _, err := ReadArchive(bytes.NewReader(good), int64(len(good))); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	_ = gz.Close()
	for _, tc := range []struct {
		name string
		body []byte
	}{{"padding", append(bytes.Clone(plain), make([]byte, 1024)...)}, {"hidden data", append(bytes.Clone(plain), 1)}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := readTarManifest(tc.body)
			if (err == nil) != (tc.name == "padding") {
				t.Fatal(err)
			}
		})
	}
	entries[1].Size = MaxManifestBytes + 1
	big := archiveFixture(t, entries, [][]byte{nil, bytes.Repeat([]byte("x"), MaxManifestBytes+1)})
	if _, err := ReadArchive(bytes.NewReader(big), int64(len(big))); err == nil {
		t.Fatal("oversized manifest")
	}
	entries[1].Size = 0
	empty := archiveFixture(t, entries, [][]byte{nil, nil})
	if _, err := ReadArchive(bytes.NewReader(empty), int64(len(empty))); err == nil {
		t.Fatal("empty manifest")
	}
}
