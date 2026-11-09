package gems

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func repackGem(t *testing.T, original []byte, change func(map[string][]byte)) []byte {
	t.Helper()
	parts := map[string][]byte{}
	r := tar.NewReader(bytes.NewReader(original))
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		parts[h.Name], err = io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
	}
	change(parts)
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, name := range []string{"metadata.gz", "data.tar.gz", "checksums.yaml.gz", "unexpected"} {
		if body, ok := parts[name]; ok {
			if err := w.WriteHeader(
				&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))},
			); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gzipBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gemParts(t *testing.T, original []byte) map[string][]byte {
	t.Helper()
	parts := map[string][]byte{}
	r := tar.NewReader(bytes.NewReader(original))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatal(err)
		}
		parts[h.Name], err = io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func gemTar(t *testing.T, entries []struct {
	name     string
	data     []byte
	typeFlag byte
},
) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		if err := w.WriteHeader(
			&tar.Header{Name: e.name, Typeflag: e.typeFlag, Mode: 0o644, Size: int64(len(e.data))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitRubyGemRejectsArchiveBoundaryViolations(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	parts := gemParts(t, fixture)
	entry := func(name string) struct {
		name     string
		data     []byte
		typeFlag byte
	} {
		return struct {
			name     string
			data     []byte
			typeFlag byte
		}{name: name, data: parts[name], typeFlag: tar.TypeReg}
	}
	for name, tc := range map[string]struct {
		body     []byte
		size     int64
		path     string
		expected string
	}{
		"zero size":               {fixture, 0, "gems/rake-12.3.1.gem", "invalid gem size or path"},
		"oversized declared size": {fixture, MaxGemBytes + 1, "gems/rake-12.3.1.gem", "invalid gem size or path"},
		"unsafe path":             {fixture, int64(len(fixture)), "gems/../rake-12.3.1.gem", "invalid gem size or path"},
		"truncated archive":       {fixture[:len(fixture)-1], int64(len(fixture)), "gems/rake-12.3.1.gem", "truncated gem archive"},
		"invalid tar":             {[]byte("invalid tar"), int64(len("invalid tar")), "", "invalid gem tar"},
		"missing metadata": {gemTar(t, []struct {
			name     string
			data     []byte
			typeFlag byte
		}{entry("data.tar.gz")}), -1, "", "missing gem metadata or data"},
		"duplicate metadata": {gemTar(t, []struct {
			name     string
			data     []byte
			typeFlag byte
		}{entry("metadata.gz"), entry("metadata.gz"), entry("data.tar.gz")}), 0, "", "duplicate or oversized gem entry"},
		"directory metadata": {gemTar(t, []struct {
			name     string
			data     []byte
			typeFlag byte
		}{{name: "metadata.gz", typeFlag: tar.TypeDir}}), 0, "", "non-file gem entry"},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.size < 0 || tc.size == 0 && name != "zero size" {
				tc.size = int64(len(tc.body))
			}
			_, err := Read(t.Context(), bytes.NewReader(tc.body), tc.size, tc.path)
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("Read() = %v, want %q", err, tc.expected)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Read(
		ctx,
		bytes.NewReader(fixture),
		int64(len(fixture)),
		"",
	); err == nil ||
		!strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("canceled archive read = %v", err)
	}
}

func mutateGemMetadata(t *testing.T, fixture []byte, change func(string) string) []byte {
	t.Helper()
	return repackGem(t, fixture, func(parts map[string][]byte) {
		z, err := gzip.NewReader(bytes.NewReader(parts["metadata.gz"]))
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := io.ReadAll(z)
		if err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		parts["metadata.gz"] = gzipBody(t, []byte(change(string(metadata))))
		delete(parts, "checksums.yaml.gz")
	})
}

func TestUnitRubyGemRejectsInvalidMetadataIdentities(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		old, replacement, expected string
	}{
		"non specification root":  {"--- !ruby/object:Gem::Specification", "--- !ruby/object:Other", "missing Gem::Specification"},
		"sequence root":           {"--- !ruby/object:Gem::Specification", "---", "missing Gem::Specification"},
		"invalid name":            {"name: rake", "name: ../escape", "gem identity differs from path"},
		"invalid version":         {"version: 12.3.1", "version: 12..3", "gem identity differs from path"},
		"invalid platform":        {"platform: ruby", "platform: ../linux", "gem identity differs from path"},
		"invalid dependency tag":  {"!ruby/object:Gem::Dependency", "!ruby/object:Other", "invalid gem dependency"},
		"invalid dependency name": {"name: bundler", "name: ../bad", "invalid gem dependency name"},
	} {
		t.Run(name, func(t *testing.T) {
			b := mutateGemMetadata(t, fixture, func(meta string) string {
				if !strings.Contains(meta, tc.old) {
					t.Fatalf("fixture missing %q", tc.old)
				}
				if name == "invalid dependency name" {
					meta = strings.Replace(meta, "type: :development", "type: :runtime", 1)
				}
				return strings.Replace(meta, tc.old, tc.replacement, 1)
			})
			_, err := Read(t.Context(), bytes.NewReader(b), int64(len(b)), "gems/rake-12.3.1.gem")
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("Read() = %v, want %q", err, tc.expected)
			}
		})
	}
	withoutOptional := mutateGemMetadata(t, fixture, func(meta string) string {
		return strings.Replace(meta, "platform: ruby", "platform: ", 1)
	})
	p, err := Read(
		t.Context(),
		bytes.NewReader(withoutOptional),
		int64(len(withoutOptional)),
		"gems/rake-12.3.1.gem",
	)
	if err != nil || p.Platform != "ruby" {
		t.Fatalf("missing optional platform = %+v, %v", p, err)
	}
}

func TestUnitRubyGemRejectsUnsafeDataTar(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]tar.Header{
		"absolute path":        {Name: "/etc/passwd", Typeflag: tar.TypeReg},
		"parent path":          {Name: "lib/..", Typeflag: tar.TypeDir},
		"absolute symlink":     {Name: "lib/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"unsupported hardlink": {Name: "lib/link", Typeflag: tar.TypeLink, Linkname: "lib/rake.rb"},
	} {
		t.Run(name, func(t *testing.T) {
			b := repackGem(t, fixture, func(parts map[string][]byte) {
				var data bytes.Buffer
				gz := gzip.NewWriter(&data)
				tarWriter := tar.NewWriter(gz)
				if err := tarWriter.WriteHeader(&h); err != nil {
					t.Fatal(err)
				}
				if err := tarWriter.Close(); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				parts["data.tar.gz"] = data.Bytes()
				delete(parts, "checksums.yaml.gz")
			})
			if _, err := Read(
				t.Context(),
				bytes.NewReader(b),
				int64(len(b)),
				"gems/rake-12.3.1.gem",
			); err == nil {
				t.Fatal("unsafe data entry was admitted")
			}
		})
	}
}

func TestUnitPinnedRakeGem(t *testing.T) {
	b, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Read(context.Background(), bytes.NewReader(b), int64(len(b)), "gems/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "rake" || p.Version != "12.3.1" || p.Platform != "ruby" ||
		p.SHA256 != "cf37840d136743411e4bd601885363e34dce537675306464528b0cafe428b1c5" {
		t.Fatalf("wrong pinned gem: %+v", p)
	}
	if _, err := Read(
		context.Background(),
		bytes.NewReader(b),
		int64(len(b)),
		"gems/rake-12.3.2.gem",
	); err == nil {
		t.Fatal("accepted wrong path")
	}
	corrupt := bytes.Clone(b)
	corrupt[len(corrupt)/2] ^= 1
	if _, err := Read(
		context.Background(),
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
		"gems/rake-12.3.1.gem",
	); err == nil {
		t.Fatal("accepted corrupt archive")
	}
}

func TestUnitRubyGemRejectsMalformedArchiveAndMetadata(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]func(map[string][]byte){
		"missing data": func(parts map[string][]byte) { delete(parts, "data.tar.gz") },
		"invalid metadata gzip": func(parts map[string][]byte) {
			parts["metadata.gz"] = []byte("not gzip")
			delete(parts, "checksums.yaml.gz")
		},
		"invalid gemspec YAML": func(parts map[string][]byte) {
			parts["metadata.gz"] = gzipBody(t, []byte("[unclosed\n"))
			delete(parts, "checksums.yaml.gz")
		},
		"invalid checksums YAML": func(parts map[string][]byte) {
			parts["checksums.yaml.gz"] = gzipBody(t, []byte("SHA256: [malformed]\n"))
		},
		"noncanonical gem version": func(parts map[string][]byte) {
			z, err := gzip.NewReader(bytes.NewReader(parts["metadata.gz"]))
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := io.ReadAll(z)
			if err != nil {
				t.Fatal(err)
			}
			_ = z.Close()
			metadata = bytes.Replace(
				metadata,
				[]byte("version: 12.3.1"),
				[]byte("version: 12.3.1-rc1"),
				1,
			)
			parts["metadata.gz"] = gzipBody(t, metadata)
			delete(parts, "checksums.yaml.gz")
		},
		"invalid data gzip":      func(parts map[string][]byte) { parts["data.tar.gz"] = []byte("not gzip") },
		"invalid checksums gzip": func(parts map[string][]byte) { parts["checksums.yaml.gz"] = []byte("not gzip") },
		"mismatched checksums": func(parts map[string][]byte) {
			parts["checksums.yaml.gz"] = gzipBody(
				t,
				[]byte("SHA256:\n  metadata.gz: 0000\n  data.tar.gz: 0000\n"),
			)
		},
		"unexpected outer entry": func(parts map[string][]byte) { parts["unexpected"] = []byte("bad") },
		"unsafe data path": func(parts map[string][]byte) {
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tr := tar.NewWriter(gz)
			if err := tr.WriteHeader(
				&tar.Header{Name: "../escape", Mode: 0o644, Size: 1},
			); err != nil {
				t.Fatal(err)
			}
			if _, err := tr.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := tr.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			parts["data.tar.gz"] = b.Bytes()
		},
		"unsafe symlink": func(parts map[string][]byte) {
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tr := tar.NewWriter(gz)
			if err := tr.WriteHeader(
				&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
			); err != nil {
				t.Fatal(err)
			}
			if err := tr.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			parts["data.tar.gz"] = b.Bytes()
		},
	}
	for name, change := range variants {
		t.Run(name, func(t *testing.T) {
			b := repackGem(t, fixture, change)
			if _, err := Read(
				t.Context(),
				bytes.NewReader(b),
				int64(len(b)),
				"gems/rake-12.3.1.gem",
			); err == nil {
				t.Fatal("accepted invalid gem")
			}
		})
	}
}

func TestUnitRubyGemRuntimeDependencyInCompactMetadata(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	b := repackGem(t, fixture, func(parts map[string][]byte) {
		z, err := gzip.NewReader(bytes.NewReader(parts["metadata.gz"]))
		if err != nil {
			t.Fatal(err)
		}
		meta, err := io.ReadAll(z)
		if err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		meta = bytes.Replace(meta, []byte("type: :development"), []byte("type: :runtime"), 1)
		parts["metadata.gz"] = gzipBody(t, meta)
		delete(parts, "checksums.yaml.gz")
	})
	p, err := Read(t.Context(), bytes.NewReader(b), int64(len(b)), "gems/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Dependencies) != 1 || p.Dependencies[0].Name != "bundler" ||
		!bytes.Contains(CompactInfo("rake", []Package{p}), []byte("bundler:>= 0")) {
		t.Fatalf(
			"runtime dependency not indexed: %+v, %s",
			p.Dependencies,
			CompactInfo("rake", []Package{p}),
		)
	}
}

func TestUnitCompactIndexRoundTrip(t *testing.T) {
	p := Package{
		Name:     "rake",
		Version:  "12.3.1",
		Platform: "ruby",
		Path:     "gems/rake-12.3.1.gem",
		SHA256:   "cf37840d136743411e4bd601885363e34dce537675306464528b0cafe428b1c5",
	}
	versions := CompactVersions([]Package{p})
	names, err := ParseCompactVersions(versions)
	if err != nil {
		t.Fatal(err)
	}
	if names["rake"].MD5 == "" || !names["rake"].Versions["12.3.1"] {
		t.Fatal("missing rake")
	}
	rows, err := ParseCompactInfo("rake", CompactInfo("rake", []Package{p}))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SHA256 != p.SHA256 {
		t.Fatalf("round trip: %+v", rows)
	}
}

func TestUnitCompactIndexRejectsMalformedMetadata(t *testing.T) {
	good := "cf37840d136743411e4bd601885363e34dce537675306464528b0cafe428b1c5"
	for _, body := range []string{
		"---\n12.3.1 |checksum:bad\n",
		"---\n12.3.1 dependency:not-a-checksum\n",
		"---\n12.3.1 |checksum:" + good + "\n12.3.1 |checksum:" + good + "\n",
		"---\n12.3.1 |checksum:" + good + ",unknown:field\n",
	} {
		if _, err := ParseCompactInfo("rake", []byte(body)); err == nil {
			t.Fatalf("accepted malformed compact info %q", body)
		}
	}
	for _, body := range []string{
		"---\nrake 12.3.1 invalid\n",
		"---\n../escape 12.3.1 0123456789abcdef0123456789abcdef\n",
	} {
		if _, err := ParseCompactVersions([]byte(body)); err == nil {
			t.Fatalf("accepted malformed compact versions %q", body)
		}
	}
}

func TestUnitRubyGemVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		a, b     string
		expected int
	}{
		{"10.0.0", "9.9.9", 1},
		{"1.0.0.pre", "1.0.0", -1},
		{"1.2", "1.10", -1},
		{"2.0", "2.0", 0},
	} {
		got := CompareVersions(tc.a, tc.b)
		if got != tc.expected {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.expected)
		}
	}
	packages := []Package{
		{Name: "sample", Version: "9.0", Platform: "ruby"},
		{Name: "sample", Version: "10.0", Platform: "ruby"},
		{Name: "sample", Version: "10.1.pre", Platform: "ruby"},
	}
	latest, err := Specs(packages, true, false)
	if err != nil || len(latest) == 0 {
		t.Fatal(err)
	}
	pre, err := Specs(packages, false, true)
	if err != nil || len(pre) == 0 {
		t.Fatal(err)
	}
}

func TestUnitRubyGemHandlesOptionalGemspecShapes(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ old, replacement string }{
		"authors not a list":          {"authors:\n- Hiroshi SHIBATA\n- Eric Hodel\n- Jim Weirich", "authors: unknown"},
		"requirements not a map":      {"required_ruby_version: !ruby/object:Gem::Requirement\n  requirements:\n  - - \">=\"\n    - !ruby/object:Gem::Version\n      version: 2.0.0", "required_ruby_version: []"},
		"requirements not a list":     {"  requirements:\n  - - \">=\"", "  requirements: unknown\n  ignored:\n  - - \">=\""},
		"malformed requirement row":   {"  - - \">=\"\n    - !ruby/object:Gem::Version\n      version: 2.0.0", "  - - \">=\""},
		"missing requirement version": {"version: 2.0.0", "other: 2.0.0"},
	} {
		t.Run(name, func(t *testing.T) {
			b := mutateGemMetadata(t, fixture, func(meta string) string {
				if !strings.Contains(meta, tc.old) {
					t.Fatalf("fixture missing %q", tc.old)
				}
				return strings.Replace(meta, tc.old, tc.replacement, 1)
			})
			p, err := Read(t.Context(), bytes.NewReader(b), int64(len(b)), "gems/rake-12.3.1.gem")
			if err != nil || p.Name != "rake" || p.Version != "12.3.1" {
				t.Fatalf("optional gemspec shape rejected: %+v, %v", p, err)
			}
		})
	}
}

func TestUnitRubyGemRejectsTruncatedTarEntry(t *testing.T) {
	var outer bytes.Buffer
	w := tar.NewWriter(&outer)
	if err := w.WriteHeader(&tar.Header{Name: "metadata.gz", Mode: 0o644, Size: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("short")); err != nil {
		t.Fatal(err)
	}
	b := outer.Bytes() // Deliberately omit tar.Close: the declared entry exceeds the data.
	if _, err := Read(
		t.Context(),
		bytes.NewReader(b),
		int64(len(b)),
		"",
	); err == nil ||
		!strings.Contains(err.Error(), "truncated gem entry") {
		t.Fatalf("truncated tar entry = %v", err)
	}
}

func TestUnitRubyGemRejectsOversizedMetadataAndTruncatedData(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rake-12.3.1.gem")
	if err != nil {
		t.Fatal(err)
	}
	oversized := repackGem(t, fixture, func(parts map[string][]byte) {
		parts["metadata.gz"] = gzipBody(t, bytes.Repeat([]byte("x"), maxMetadataBytes+1))
		delete(parts, "checksums.yaml.gz")
	})
	if _, err := Read(
		t.Context(),
		bytes.NewReader(oversized),
		int64(len(oversized)),
		"",
	); err == nil ||
		!strings.Contains(err.Error(), "metadata exceeds limit") {
		t.Fatalf("oversized metadata = %v", err)
	}
	truncated := repackGem(t, fixture, func(parts map[string][]byte) {
		var data bytes.Buffer
		w := tar.NewWriter(&data)
		if err := w.WriteHeader(
			&tar.Header{Name: "lib/rake.rb", Mode: 0o644, Size: 10},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("short")); err != nil {
			t.Fatal(err)
		}
		parts["data.tar.gz"] = gzipBody(t, data.Bytes())
		delete(parts, "checksums.yaml.gz")
	})
	if _, err := Read(
		t.Context(),
		bytes.NewReader(truncated),
		int64(len(truncated)),
		"",
	); err == nil ||
		!strings.Contains(err.Error(), "truncated gem data entry") {
		t.Fatalf("truncated gem data = %v", err)
	}
}

func TestAccRubyClientLoadsMarshalIndexes(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby client unavailable")
	}
	p := Package{
		Name:     "rake",
		Version:  "12.3.1",
		Platform: "ruby",
		Summary:  "Rake",
		SHA256:   "cf37840d136743411e4bd601885363e34dce537675306464528b0cafe428b1c5",
	}
	specs, err := Specs([]Package{p}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	quick, err := QuickSpec(p, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "specs.gz"), specs, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quick.rz"), quick, 0o600); err != nil {
		t.Fatal(err)
	}
	code := `require 'zlib'; require 'rubygems'; specs=Marshal.load(Zlib::GzipReader.open(ARGV[0]).read); raise specs.inspect unless specs[0][0]=='rake' && specs[0][1].to_s=='12.3.1'; s=Marshal.load(Zlib::Inflate.inflate(File.binread(ARGV[1]))); raise s.inspect unless s.is_a?(Gem::Specification) && s.name=='rake' && s.version.to_s=='12.3.1'`
	cmd := exec.Command(
		"ruby",
		"-e",
		code,
		filepath.Join(dir, "specs.gz"),
		filepath.Join(dir, "quick.rz"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ruby cannot load native index: %v: %s", err, out)
	}
}

func TestAccRubyClientLoadsRichGemspec(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby client unavailable")
	}
	p := Package{
		Name:         "sample",
		Version:      "10.2.0",
		Platform:     "x86_64-linux",
		Summary:      "Sample gem",
		Description:  "A native gemspec compatibility check",
		Homepage:     "https://example.invalid/sample",
		Authors:      []string{"Alice"},
		Licenses:     []string{"MIT"},
		Ruby:         ">= 2.7",
		RubyGems:     ">= 3.0",
		Dependencies: []Dependency{{Name: "rake", Requirement: ">= 12.0, < 14.0"}},
	}
	quick, err := QuickSpec(p, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range map[string][]byte{
		"quick.rz":         quick,
		"negative.marshal": Marshal(-128),
		"map.marshal":      Marshal(map[string]string{"license": "MIT"}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// RubyGems 3.0 loads its own licenses field into @license while newer
	// releases expose it through licenses. Compare with the local client's
	// native Marshal round trip so both versions check the same wire behavior.
	code := `require 'zlib'; require 'rubygems'; s=Marshal.load(Zlib::Inflate.inflate(File.binread(ARGV[0]))); native=Gem::Specification.new{|x|x.name='sample';x.version='10.2.0';x.licenses=['MIT']}; native=Marshal.load(Marshal.dump(native)); raise s.inspect unless s.is_a?(Gem::Specification) && s.name=='sample' && s.version.to_s=='10.2.0' && s.platform.to_s=='x86_64-linux' && s.authors==['Alice'] && s.licenses==native.licenses && s.instance_variable_get(:@license)==native.instance_variable_get(:@license) && s.dependencies.length==1 && s.dependencies[0].name=='rake' && s.dependencies[0].requirement.to_s=='>= 12.0, < 14.0' && s.required_ruby_version.to_s=='>= 2.7' && s.required_rubygems_version.to_s=='>= 3.0'; raise unless Marshal.load(File.binread(ARGV[1])) == -128; raise unless Marshal.load(File.binread(ARGV[2])) == {'license'=>'MIT'}`
	cmd := exec.Command(
		"ruby",
		"-e",
		code,
		filepath.Join(dir, "quick.rz"),
		filepath.Join(dir, "negative.marshal"),
		filepath.Join(dir, "map.marshal"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Ruby rejected rich gemspec: %v: %s", err, out)
	}
}

func TestAccRubyClientLoadsDefaultPlatformAndRequirements(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("Ruby client unavailable")
	}
	quick, err := QuickSpec(
		Package{Name: "sample", Version: "1.0.0", Ruby: "malformed"},
		time.Unix(0, 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "default.gemspec.rz")
	if err := os.WriteFile(path, quick, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(
		"ruby",
		"-rzlib",
		"-rrubygems",
		"-e",
		`spec=Marshal.load(Zlib::Inflate.inflate(File.binread(ARGV[0]))); raise spec.inspect unless spec.platform.to_s=='ruby' && spec.required_ruby_version.to_s=='>= 0'`,
		path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Ruby rejected default gemspec: %v: %s", err, output)
	}
}
