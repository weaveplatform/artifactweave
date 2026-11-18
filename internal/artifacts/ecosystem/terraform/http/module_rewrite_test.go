package terraformhttp

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func moduleZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnitRegistrySourceRewriting(t *testing.T) {
	target := rewriteTarget{Host: "proxy.example:8443", Repo: "all"}
	for _, input := range []string{"acme/helper/aws", "registry.private.example/acme/helper/aws//modules/nested", "acme__team/helper/aws"} {
		got := target.address(input)
		if !strings.HasPrefix(got, "proxy.example:8443/all__acme") || strings.Contains(input, "//modules/") && !strings.HasSuffix(got, "//modules/nested") {
			t.Fatalf("%s → %s", input, got)
		}
	}
	for _, input := range []string{"./local", "../sibling", "git::https://example.test/project.git//modules/a", "https://example.test/archive.zip", "github.com/acme/project", "s3::https://example.test/a", "proxy.example:8443/all__acme/helper/aws"} {
		if got := target.address(input); got != input {
			t.Fatalf("non-registry/already routed source changed: %s → %s", input, got)
		}
	}
	input := `# formatting retained
module "dependency" {
  source = "registry.private.example/acme/helper/aws//modules/deep"
  version = "1.0.0"
}
resource "example" "x" { source = "acme/other/aws" }
module "relative" { source = "../other" }
module "dynamic" { source = var.name }
`
	out, changed := rewriteModuleFile("main.tf", []byte(input), target)
	if !changed || !strings.Contains(string(out), `source = "proxy.example:8443/all__acme/helper/aws//modules/deep"`) ||
		!strings.Contains(string(out), `source = "acme/other/aws"`) ||
		!bytes.HasPrefix(out, []byte("# formatting retained\n")) {
		t.Fatal(string(out))
	}
	for _, name := range []string{"README.md", "broken.tf"} {
		body := []byte(`not HCL`)
		if got, changed := rewriteModuleFile(name, body, target); changed || !bytes.Equal(got, body) {
			t.Fatal(name, string(got))
		}
	}
	inputJSON := `{"module":{"child":{"source":"acme/helper/aws","version":"1.0.0"}},"resource":{"example":{"x":{"source":"acme/other/aws"}}}}`
	for _, input := range []string{inputJSON, "[" + inputJSON + "]", `{"module":[{"child":[{"source":"acme/helper/aws"}]}]}`} {
		out, changed := rewriteModuleFile("main.tf.json", []byte(input), target)
		if !changed || !strings.Contains(string(out), "proxy.example:8443/all__acme/helper/aws") {
			t.Fatal(string(out))
		}
	}
	out, changed = rewriteModuleFile("modules/main.tf.json", []byte(inputJSON), target)
	if !changed || !strings.Contains(string(out), "proxy.example:8443/all__acme/helper/aws") || !strings.Contains(string(out), `"source":"acme/other/aws"`) {
		t.Fatal(string(out))
	}
}

func TestUnitModuleArchiveRewriteFormatsAndLinks(t *testing.T) {
	config := `module "child" { source = "acme/helper/aws//modules/nested" }`
	var tarBytes bytes.Buffer
	tw := tar.NewWriter(&tarBytes)
	for _, header := range []*tar.Header{{Name: "root/main.tf", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(config))}, {Name: "root/link.tf", Typeflag: tar.TypeSymlink, Linkname: "main.tf", Mode: 0o777}, {Name: "root/hard.tf", Typeflag: tar.TypeLink, Linkname: "root/main.tf", Mode: 0o640}} {
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(config)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var xzBytes bytes.Buffer
	xw, err := xz.NewWriter(&xzBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = xw.Write(tarBytes.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = xw.Close(); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"zip":    moduleZip(t, map[string]string{"root/main.tf": config}),
		"tar":    tarBytes.Bytes(),
		"tar.gz": archiveGzip(t, tarBytes.Bytes()),
		"tar.xz": xzBytes.Bytes(),
	}
	cases["tar.bz2"] = repopath.Read(t, "internal/artifacts/ecosystem/terraform/http/testdata/module-sources.tar.bz2")
	for kind, body := range cases {
		t.Run(kind, func(t *testing.T) {
			var delivered bytes.Buffer
			format, changed, err := rewriteModuleArchive(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
				moduleSource{Archive: kind, Subdir: "root", Target: "https://example.test/archive." + kind},
				rewriteTarget{Host: "registry.test", Repo: "virtual"},
				&delivered,
			)
			// A literal selected directory is resolved before handing a getter URL out.
			if err != nil || !changed {
				t.Fatalf("rewrite %s: changed=%v err=%v", format, changed, err)
			}
			if err := validateCompleteArchive(
				t.Context(),
				bytes.NewReader(delivered.Bytes()),
				int64(delivered.Len()),
				"module."+format,
				remoteArchiveEntries,
				remoteArchiveInflated,
			); err != nil {
				t.Fatal(err)
			}
			if kind == "zip" {
				zr, err := zip.NewReader(bytes.NewReader(delivered.Bytes()), int64(delivered.Len()))
				if err != nil {
					t.Fatal(err)
				}
				r, err := zr.File[0].Open()
				if err != nil {
					t.Fatal(err)
				}
				out, _ := io.ReadAll(r)
				_ = r.Close()
				if !strings.Contains(string(out), "registry.test/virtual__acme/helper/aws//modules/nested") {
					t.Fatal(string(out))
				}
			} else {
				gz, err := gzip.NewReader(bytes.NewReader(delivered.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				tr := tar.NewReader(gz)
				count := 0
				for {
					header, err := tr.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					count++
					if header.Typeflag == tar.TypeReg {
						out, _ := io.ReadAll(tr)
						if !strings.Contains(string(out), "registry.test/virtual__acme/helper/aws//modules/nested") {
							t.Fatal(string(out))
						}
					}
					if header.Typeflag == tar.TypeSymlink && header.Linkname != "main.tf" {
						t.Fatal(header)
					}
					if header.Typeflag == tar.TypeLink && header.Linkname != "root/main.tf" {
						t.Fatal(header)
					}
				}
				if count != 3 {
					t.Fatal(count)
				}
			}
		})
	}
}

func TestAccRemoteAndVirtualModuleRewriteRetainsRawArchive(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "virtual"}[virtual], func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			raw := moduleZip(
				t,
				map[string]string{
					"main.tf":  `module "child" { source = "elsewhere.invalid/acme/helper/aws//modules/child" }`,
					"other.tf": `resource "example" "x" { source = "acme/untouched/aws" }`,
				},
			)
			f.up.Set("/archives/1.0.0.zip", fakeupstream.Resource{Body: raw, Header: http.Header{"Content-Type": {"application/zip"}}})
			base := remoteModuleBase
			if virtual {
				virtualMembers(t, f.fixture, "remote")
				base = prefix + "v1/modules/virtual__ns/name/aws/"
			}
			metadata := f.do("GET", base+"1.0.0/download", "reader")
			expect(t, metadata, 204)
			public := metadata.Header().Get("X-Terraform-Get")
			result := f.do("GET", public, "")
			expect(t, result, 200)
			if bytes.Equal(raw, result.Body.Bytes()) {
				t.Fatal("raw source was delivered without source rewriting")
			}
			key := "remote"
			if virtual {
				key = "virtual"
			}
			zr, err := zip.NewReader(bytes.NewReader(result.Body.Bytes()), int64(result.Body.Len()))
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range zr.File {
				r, _ := file.Open()
				body, _ := io.ReadAll(r)
				_ = r.Close()
				if file.Name == "main.tf" && !strings.Contains(string(body), "registry.test/"+key+"__acme/helper/aws//modules/child") {
					t.Fatal(string(body))
				}
			}
			rawItem, err := f.db.Items().Get(t.Context(), "remote", true, "archives/1.0.0.zip")
			if err != nil {
				t.Fatal(err)
			}
			opened, _, err := f.items.Blobs.Open(t.Context(), rawItem.Actual.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			cached, _ := io.ReadAll(opened)
			_ = opened.Close()
			if !bytes.Equal(raw, cached) {
				t.Fatal("raw source cache was mutated")
			}
			if result.Header().Get("X-Checksum-Sha256") == rawItem.Actual.SHA256 || result.Header().Get("Content-Length") == "" {
				t.Fatal(result.Header())
			}
			req := httptest.NewRequest("GET", public, nil)
			req.Header.Set("If-None-Match", result.Header().Get("ETag"))
			response := httptest.NewRecorder()
			f.h.ServeHTTP(response, req)
			expect(t, response, 304)
			req = httptest.NewRequest("GET", public, nil)
			req.Header.Set("Range", "bytes=0-9")
			response = httptest.NewRecorder()
			f.h.ServeHTTP(response, req)
			expect(t, response, 206)
			if !bytes.Equal(response.Body.Bytes(), result.Body.Bytes()[:10]) {
				t.Fatal("range did not describe derived representation")
			}
			u, _ := url.Parse(public)
			q := u.Query()
			q.Set("archive", "tar.gz")
			u.RawQuery = q.Encode()
			expect(t, f.do("GET", u.String(), ""), 403)
		})
	}
}

func TestUnitUnchangedModuleNeedsNoTemporaryArchive(t *testing.T) {
	body := moduleZip(t, map[string]string{"main.tf": `module "child" { source = "../relative" }`})
	t.Setenv("TMPDIR", t.TempDir()+"/absent")
	result := source.Result{Stream: &source.Stream{Body: &moduleBody{bytes.NewReader(body)}, Representation: source.Representation{Size: int64(len(body))}}}
	h := handler{}
	rewritten, err := h.rewriteModule(
		httptest.NewRequest("GET", "https://registry.test/", nil),
		result,
		moduleSource{Archive: "zip"},
		rewriteTarget{Host: "registry.test", Repo: "virtual"},
	)
	if err != nil || rewritten.digest != "" || rewritten.result.Stream != nil || rewritten.archive != "zip" {
		t.Fatalf("unchanged archive needed output: %#v %v", rewritten, err)
	}
	remaining, err := io.ReadAll(result.Stream.Body)
	if err != nil || !bytes.Equal(remaining, body) {
		t.Fatal("original archive not rewound", err)
	}
}

func TestAccModuleRewriteKeepsSignedPublicProxyTarget(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	virtualMembers(t, f.fixture, "remote")
	f.up.Set(
		"/archives/1.0.0.zip",
		fakeupstream.Resource{
			Body:   moduleZip(t, map[string]string{"main.tf": `module "dependency" { source = "acme/helper/aws" }`}),
			Header: http.Header{"Content-Type": {"application/zip"}},
		},
	)
	request := httptest.NewRequest("GET", prefix+"virtual/v1/modules/ns/name/aws/1.0.0/download", nil)
	request.Header.Set("X-User", "reader")
	request.Header.Set(baseurl.OverrideHeader, "https://proxy.example:8443")
	metadata := httptest.NewRecorder()
	f.h.ServeHTTP(metadata, request)
	expect(t, metadata, 204)
	public := metadata.Header().Get("X-Terraform-Get")
	if !strings.HasPrefix(public, "https://proxy.example:8443/") {
		t.Fatal(public)
	}
	request = httptest.NewRequest("GET", public, nil)
	request.Header.Set(baseurl.OverrideHeader, "https://different.example")
	delivered := httptest.NewRecorder()
	f.h.ServeHTTP(delivered, request)
	expect(t, delivered, 200)
	zr, err := zip.NewReader(bytes.NewReader(delivered.Body.Bytes()), int64(delivered.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	file, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "proxy.example:8443/virtual__acme/helper/aws") || strings.Contains(string(body), "different.example") {
		t.Fatal(string(body))
	}
}
