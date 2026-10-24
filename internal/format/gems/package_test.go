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
		"missing data":          func(parts map[string][]byte) { delete(parts, "data.tar.gz") },
		"invalid metadata gzip": func(parts map[string][]byte) { parts["metadata.gz"] = []byte("not gzip") },
		"invalid gemspec YAML":  func(parts map[string][]byte) { parts["metadata.gz"] = gzipBody(t, []byte("name: rake\n")) },
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
		a, b string
		want int
	}{
		{"10.0.0", "9.9.9", 1},
		{"1.0.0.pre", "1.0.0", -1},
		{"1.2", "1.10", -1},
		{"2.0", "2.0", 0},
	} {
		got := CompareVersions(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
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
		"negative.marshal": Marshal(-1),
		"map.marshal":      Marshal(map[string]string{"license": "MIT"}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// RubyGems 3.0 loads its own licenses field into @license while newer
	// releases expose it through licenses. Compare with the local client's
	// native Marshal round trip so both versions check the same wire behavior.
	code := `require 'zlib'; require 'rubygems'; s=Marshal.load(Zlib::Inflate.inflate(File.binread(ARGV[0]))); native=Gem::Specification.new{|x|x.name='sample';x.version='10.2.0';x.licenses=['MIT']}; native=Marshal.load(Marshal.dump(native)); raise s.inspect unless s.is_a?(Gem::Specification) && s.name=='sample' && s.version.to_s=='10.2.0' && s.platform.to_s=='x86_64-linux' && s.authors==['Alice'] && s.licenses==native.licenses && s.instance_variable_get(:@license)==native.instance_variable_get(:@license) && s.dependencies.length==1 && s.dependencies[0].name=='rake' && s.dependencies[0].requirement.to_s=='>= 12.0, < 14.0' && s.required_ruby_version.to_s=='>= 2.7' && s.required_rubygems_version.to_s=='>= 3.0'; raise unless Marshal.load(File.binread(ARGV[1])) == -1; raise unless Marshal.load(File.binread(ARGV[2])) == {'license'=>'MIT'}`
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
