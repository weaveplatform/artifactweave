package cocoapods

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
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

func TestUnitHostedPodArchiveAcceptsDirectoriesButRejectsBrokenTar(t *testing.T) {
	good := packed(t,
		[]tar.Header{
			{Name: "Sources/", Typeflag: tar.TypeDir},
			{Name: "HelloPod.podspec.json", Typeflag: tar.TypeReg},
		},
		[]string{"", `{"name":"HelloPod","version":"1.0.0"}`},
	)
	if _, err := ReadArchive(
		bytes.NewReader(good),
		int64(len(good)),
		"HelloPod",
		"1.0.0",
	); err != nil {
		t.Fatalf("valid pod directory refused: %v", err)
	}
	var broken bytes.Buffer
	gz := gzip.NewWriter(&broken)
	if _, err := gz.Write([]byte("this is not a tar header")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArchive(
		bytes.NewReader(broken.Bytes()),
		int64(broken.Len()),
		"HelloPod",
		"1.0.0",
	); err == nil {
		t.Fatal("gzip data without a valid tar archive was admitted")
	}
}

func TestUnitHTTPSourceRejectsOversizedDeclaredExpansion(t *testing.T) {
	var tarBody bytes.Buffer
	gz := gzip.NewWriter(&tarBody)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Sources/huge.m", Typeflag: tar.TypeReg, Size: maxExpandedBytes + 1,
	}); err != nil {
		t.Fatal(err)
	}
	// The member's declared size is enough to reject a bomb; no large body is needed.
	_ = tw.Close()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSourceArchive(
		bytes.NewReader(tarBody.Bytes()),
		int64(tarBody.Len()),
		".tgz",
	); err == nil {
		t.Fatal("oversized tar expansion was admitted")
	}
	var zipBody bytes.Buffer
	zw := zip.NewWriter(&zipBody)
	f, err := zw.Create("Sources/huge.m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("tiny")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipData := zipBody.Bytes()
	central := bytes.Index(zipData, []byte("PK\x01\x02"))
	if central < 0 {
		t.Fatal("ZIP central directory missing")
	}
	binary.LittleEndian.PutUint32(zipData[central+24:], uint32(maxExpandedBytes+1))
	if err := ValidateSourceArchive(
		bytes.NewReader(zipData),
		int64(len(zipData)),
		".zip",
	); err == nil {
		t.Fatal("oversized zip expansion was admitted")
	}
	brokenHeader := bytes.Clone(zipBody.Bytes())
	binary.LittleEndian.PutUint32(brokenHeader[central+24:], 4)
	copy(brokenHeader[:4], []byte("bad!"))
	if err := ValidateSourceArchive(
		bytes.NewReader(brokenHeader),
		int64(len(brokenHeader)),
		".zip",
	); err == nil {
		t.Fatal("zip with a corrupt member header was admitted")
	}
}

func TestUnitPodAndSourceArchivesRejectTruncatedMembers(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
	}{
		{"podspec", "HelloPod.podspec.json"},
		{"source", "Sources/HelloPod.m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			gz := gzip.NewWriter(&body)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{
				Name: tc.file, Typeflag: tar.TypeReg, Size: 128,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("incomplete")); err != nil {
				t.Fatal(err)
			}
			_ = tw.Close() // The deliberately incomplete member cannot close as a valid tar.
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			data := body.Bytes()
			if _, err := ReadArchive(
				bytes.NewReader(data),
				int64(len(data)),
				"HelloPod",
				"1.0.0",
			); err == nil {
				t.Fatal("truncated hosted pod archive admitted")
			}
			if err := ValidateSourceArchive(
				bytes.NewReader(data),
				int64(len(data)),
				".tgz",
			); err == nil {
				t.Fatal("truncated remote source archive admitted")
			}
		})
	}
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

func TestUnitHTTPSourceArchiveAdmissionBoundaries(t *testing.T) {
	valid := packed(
		t,
		[]tar.Header{{Name: "Sources/HelloPod.m", Typeflag: tar.TypeReg}},
		[]string{"source"},
	)
	for _, tc := range []struct {
		name string
		body []byte
		size int64
		ext  string
	}{
		{"empty", valid, 0, ".tgz"},
		{"oversized", valid, MaxArchiveBytes + 1, ".tgz"},
		{"truncated", valid, int64(len(valid) + 1), ".tgz"},
		{"unsupported extension", valid, int64(len(valid)), ".rar"},
		{"invalid gzip", []byte("not gzip"), 8, ".tgz"},
		{"invalid zip", []byte("not zip"), 7, ".zip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSourceArchive(bytes.NewReader(tc.body), tc.size, tc.ext); err == nil {
				t.Fatal("unsafe source archive admitted")
			}
		})
	}
	for _, name := range []string{"", "/absolute", "a\\b", "a\x00b", "a/../b", "a/./b", "a//b"} {
		if safeArchivePath(name) {
			t.Errorf("unsafe archive member %q accepted", name)
		}
	}
	for _, tc := range []struct {
		name    string
		headers []tar.Header
		body    []string
	}{
		{"empty tar", nil, nil},
		{"duplicate tar", []tar.Header{{Name: "a", Typeflag: tar.TypeReg}, {Name: "a", Typeflag: tar.TypeReg}}, []string{"x", "y"}},
		{"symlink tar", []tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "outside"}}, []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := packed(t, tc.headers, tc.body)
			if err := ValidateSourceArchive(
				bytes.NewReader(data),
				int64(len(data)),
				".tar.gz",
			); err == nil {
				t.Fatal("unsafe tar admitted")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"empty zip", nil},
		{"duplicate zip", []string{"same", "same"}},
		{"traversal zip", []string{"../outside"}},
		{"directory zip", []string{"Sources/", "Sources/HelloPod.m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			writer := zip.NewWriter(&out)
			for _, name := range tc.files {
				entry, err := writer.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasSuffix(name, "/") {
					if _, err := entry.Write([]byte("x")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			err := ValidateSourceArchive(bytes.NewReader(out.Bytes()), int64(out.Len()), ".zip")
			if tc.name == "directory zip" {
				if err != nil {
					t.Fatalf("safe directory member rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe zip admitted")
			}
		})
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
	if ValidName("Hello\x7fPod") {
		t.Fatal("control character in public pod name admitted")
	}
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
	if _, err := ReadArchive(strings.NewReader("not gzip"), 8, "HelloPod", "1.0.0"); err == nil {
		t.Fatal("non-gzip hosted archive admitted")
	}
	for _, tc := range []struct {
		name    string
		headers []tar.Header
		bodies  []string
	}{
		{"no root spec", []tar.Header{{Name: "Sources/a.m", Typeflag: tar.TypeReg}}, []string{"source"}},
		{"empty json spec", []tar.Header{{Name: "HelloPod.podspec.json", Typeflag: tar.TypeReg}}, []string{"{}"}},
		{"malformed json spec", []tar.Header{{Name: "HelloPod.podspec.json", Typeflag: tar.TypeReg}}, []string{"{"}},
		{"two root specs", []tar.Header{{Name: "HelloPod.podspec.json", Typeflag: tar.TypeReg}, {Name: "HelloPod.podspec", Typeflag: tar.TypeReg}}, []string{spec, "Pod::Spec.new do |s|\nend"}},
		{"duplicate source member", []tar.Header{{Name: "Sources/a.m", Typeflag: tar.TypeReg}, {Name: "Sources/a.m", Typeflag: tar.TypeReg}}, []string{"source", "other"}},
		{"absolute member", []tar.Header{{Name: "/Sources/a.m", Typeflag: tar.TypeReg}}, []string{"source"}},
		{"backslash member", []tar.Header{{Name: "Sources\\a.m", Typeflag: tar.TypeReg}}, []string{"source"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := packed(t, tc.headers, tc.bodies)
			if _, err := ReadArchive(
				bytes.NewReader(body),
				int64(len(body)),
				"HelloPod",
				"1.0.0",
			); err == nil {
				t.Fatal("unsafe hosted archive admitted")
			}
		})
	}
}
