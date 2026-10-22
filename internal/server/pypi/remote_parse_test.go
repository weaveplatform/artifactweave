package pypi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const (
	indexBase = "https://example.com/simple/demo/"
	distName  = "demo-1.0-py3-none-any.whl"
)

func TestRemoteIndexParserRejectsInvalidSchemaAndLinks(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"meta":{"api-version":"1.foo"},"name":"demo","files":[]}`, `{"meta":{"api-version":"2.0"},"name":"demo","files":[]}`, `{"meta":{"api-version":"1.0"},"name":"else","files":[]}`, `{"meta":{"api-version":"1.0"},"name":"demo","files":null}`,
		`<html><a href="x">`, `<html><a href="x"><a href="y">nested</a></html>`, `<html></a></html>`, `<html><base href="http://other"></html>`, `<html><a href="x" href="y">x</a></html>`, `<html><a href="../demo-1.0-py3-none-any.whl" data-core-metadata="bad">demo-1.0-py3-none-any.whl</a></html>`, `<html><a href="https://example.com/demo-1.0-py3-none-any.whl" data-core-metadata="sha256=bad">demo-1.0-py3-none-any.whl</a></html>`,
	} {
		if _, err := parseRemoteIndex(strings.NewReader(raw), indexBase, "demo"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, href := range []string{"", "file:///demo-1.0-py3-none-any.whl", "http://example.com/demo-1.0-py3-none-any.whl", "https://user:secret@example.com/demo-1.0-py3-none-any.whl", "https://example.com/%2e%2e/demo-1.0-py3-none-any.whl", "https://example.com/other.whl", "https://example.com/demo-1.0-py3-none-any.whl#garbage", "https://example.com/demo-1.0-py3-none-any.whl#unknown=123", "https://example.com/demo-1.0-py3-none-any.whl\n", "https://%"} {
		if _, err := newRemoteFile(indexBase, "demo", distName, href, nil); err == nil {
			t.Fatalf("accepted %q", href)
		}
	}
	for key, raw := range map[string]string{"filename": "true", "url": "null", "hashes": "null", "requires-python": "true", "yanked": "null", "core-metadata": "{}"} {
		d := remoteDoc{
			"filename": encode(distName),
			"url":      encode("/files/" + distName),
			"hashes":   encode(map[string]string{}),
		}
		d[key] = json.RawMessage(raw)
		if _, err := parseJSONFile(d, indexBase, "demo"); err == nil {
			t.Fatalf("accepted %s=%s", key, raw)
		}
	}
	for _, name := range []string{"", "../demo.whl", "bad\\path.whl", "demo--py3-none-any.whl", "demo-1.0.whl", "other-1.0.tar.gz", "demo.tar.gz", "demo-..-py3-none-any.whl"} {
		if _, err := newRemoteFile(indexBase, "demo", name, "/"+name, nil); err == nil {
			t.Fatalf("accepted filename %q", name)
		}
	}
	if _, err := parseRemoteIndex(
		iotest.ErrReader(io.ErrUnexpectedEOF),
		indexBase,
		"demo",
	); err == nil {
		t.Fatal("reader error ignored")
	}
}

func TestRemoteIndexRepresentationsPreserveSemantics(t *testing.T) {
	sum := strings.Repeat("a", 64)
	html := `<!DOCTYPE html><html><a href="../../files/` + distName + `#sha256=` + sum + `" data-yanked="" data-core-metadata="true" data-gpg-sig="true" data-requires-python="&gt;=3.8">` + distName + `</a></html>`
	idx, err := parseRemoteIndex(strings.NewReader(html), indexBase, "demo")
	if err != nil {
		t.Fatal(err)
	}
	out := remoteProjectJSON(idx, idx.files, "demo", "https://proxy/api", true)
	for _, want := range []string{`"yanked":true`, `"core-metadata":true`, `"requires-python":">=3.8"`, "#sha256=" + sum} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
	if !bytes.Contains(
		remoteHTML(idx.files, "https://proxy/api", true),
		[]byte(`data-gpg-sig="true"`),
	) {
		t.Fatal("GPG declaration dropped")
	}
	noCore := remoteProjectJSON(idx, idx.files, "demo", "https://proxy/api", false)
	if bytes.Contains(noCore, []byte("core-metadata")) {
		t.Fatal("disabled metadata advertised")
	}
	for _, raw := range []string{`<html><a href="./Demo/">Demo</a></html>`, `{"meta":{"api-version":"1.0"},"projects":[{"name":"Demo"}]}`} {
		list, err := parseRemoteIndex(strings.NewReader(raw), "https://example.com/simple/", "")
		if err != nil || len(list.projects) != 1 || list.projects[0] != "demo" {
			t.Fatalf("list %+v %v", list, err)
		}
	}
	for _, raw := range []string{`<html><a href="other/">Demo</a></html>`, `<html><a href="./">bad/name</a></html>`, `{"meta":{"api-version":"1.0"},"projects":null}`, `{"meta":{"api-version":"1.0"},"projects":[{"name":"bad/name"}]}`} {
		if _, err := parseRemoteIndex(strings.NewReader(raw), indexBase, ""); err == nil {
			t.Fatalf("accepted project list %s", raw)
		}
	}
	idx.files = append(idx.files, idx.files[0])
	if uniqueFiles(idx.files) == nil {
		t.Fatal("duplicate filename accepted")
	}
	for _, v := range []string{`true`, `false`, `"reason"`} {
		d := remoteDoc{
			"filename":           encode(distName),
			"url":                encode("/files/" + distName),
			"hashes":             encode(map[string]string{}),
			"yanked":             json.RawMessage(v),
			"dist-info-metadata": json.RawMessage(`false`),
		}
		if _, err := parseJSONFile(d, indexBase, "demo"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoteHashAlgorithmsAndSidecarValidation(t *testing.T) {
	data := []byte("verified body")
	for _, algorithm := range hashNames {
		h := hashFor(algorithm)
		_, _ = h.Write(data)
		hashes := map[string]string{algorithm: hex.EncodeToString(h.Sum(nil))}
		if validateHashes(hashes) != nil || verifyHashes(bytes.NewReader(data), hashes) != nil {
			t.Fatal(algorithm)
		}
		if verifyHashes(strings.NewReader("tampered"), hashes) == nil {
			t.Fatalf("accepted tamper %s", algorithm)
		}
	}
	if verifyHashes(iotest.ErrReader(io.ErrUnexpectedEOF), map[string]string{}) == nil {
		t.Fatal("read error ignored")
	}
	if verifyHashes(bytes.NewReader(data), map[string]string{"unknown": "00"}) == nil {
		t.Fatal("unknown accepted")
	}
	for _, hashes := range []map[string]string{{"unknown": "00"}, {"sha256": "bad"}, {"sha256": "gg"}} {
		if validateHashes(hashes) == nil {
			t.Fatal("invalid digest accepted")
		}
	}
	f := remoteFile{path: "demo/1.0/" + distName, version: "1.0"}
	for _, raw := range []string{"not a header", "Name: wrong\nVersion: 1.0\n\n", "Name: demo\nVersion: 2.0\n\n", "Name: demo\nName: other\nVersion: 1.0\n\n"} {
		if f.validateCore(strings.NewReader(raw)) == nil {
			t.Fatalf("accepted sidecar %q", raw)
		}
	}
	if err := f.validateCore(
		strings.NewReader("Metadata-Version: 2.4\nName: demo\nVersion: 1.0\n\n"),
	); err != nil {
		t.Fatal(err)
	}
	if f.validateCore(iotest.ErrReader(io.ErrUnexpectedEOF)) == nil {
		t.Fatal("sidecar read error")
	}
	if f.validateArtifact(context.Background(), bytes.NewReader(data), int64(len(data))) == nil {
		t.Fatal("bad archive admitted")
	}
}

func TestRemoteConfigurationAndWarehouseValidation(t *testing.T) {
	c := repo.Config{
		PyPIRegistryURL:      "https://registry.example/root",
		PyPIRepositorySuffix: "custom/+simple",
	}
	got, err := remoteIndexURL(c, "demo")
	if err != nil || got != "https://registry.example/root/custom/+simple/demo/" {
		t.Fatalf("target %q %v", got, err)
	}
	c.PyPIRepositorySuffix = ""
	got, err = remoteIndexURL(c, "")
	if err != nil || !strings.HasSuffix(got, "/simple/") {
		t.Fatal(got, err)
	}
	for _, suffix := range []string{"../simple", "bad//simple", "simple#fragment", "simple?query"} {
		c.PyPIRepositorySuffix = suffix
		if _, err = remoteIndexURL(c, ""); err == nil {
			t.Fatalf("accepted suffix %s", suffix)
		}
	}
	for _, raw := range []string{`null`, `{"info":{"name":"other","version":"1.0"},"urls":[]}`, `{"info":{"name":"demo","version":"1.0"},"urls":null}`, `{"info":{"name":"demo","version":"1.0"},"urls":[],"releases":null}`, `{"info":{"name":"demo","version":"1.0"},"urls":[],"releases":{"1.0":null}}`} {
		if _, err := parseWarehouse(strings.NewReader(raw), indexBase, "demo", ""); err == nil {
			t.Fatalf("accepted warehouse %s", raw)
		}
	}
	if _, err := parseWarehouse(
		iotest.ErrReader(io.ErrUnexpectedEOF),
		indexBase,
		"demo",
		"",
	); err == nil {
		t.Fatal("read failure ignored")
	}
	if _, err := warehouseFiles(
		encode(
			[]any{
				map[string]any{
					"filename": distName,
					"url":      "/files/" + distName,
					"digests":  map[string]string{},
				},
			},
		),
		indexBase,
		"demo",
		"2.0",
	); err == nil {
		t.Fatal("wrong release accepted")
	}
}

func TestRemoteSignatureValidation(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	sig := key.Sign([]byte("distribution"))
	var armored bytes.Buffer
	w, err := armor.Encode(&armored, "PGP SIGNATURE", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(sig)
	_ = w.Close()
	for _, b := range [][]byte{sig, armored.Bytes()} {
		if err = validateRemoteSignature(
			context.Background(),
			bytes.NewReader(b),
			int64(len(b)),
		); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range [][]byte{nil, []byte("bad"), sig[:4], []byte("-----BEGIN PGP SIGNATURE-----"), append(append([]byte{}, armored.Bytes()...), []byte("extra")...), bytes.ReplaceAll(armored.Bytes(), []byte("PGP SIGNATURE"), []byte("PGP MESSAGE"))} {
		if validateRemoteSignature(context.Background(), bytes.NewReader(b), int64(len(b))) == nil {
			t.Fatal("invalid signature admitted")
		}
	}
	if validateRemoteSignature(context.Background(), bytes.NewReader(sig), 2<<20) == nil {
		t.Fatal("oversize signature admitted")
	}
}

func TestRemoteHTMLBaseResolvesAgainstRedirectedResponse(t *testing.T) {
	for _, tc := range []struct{ base, target string }{
		{"../files/", "https://cdn.example/new/files/"},
		{"https://cdn2.example/files/", "https://cdn2.example/files/"},
	} {
		raw := `<html><head><base href="` + tc.base + `"></head><a href="` + distName + `">` + distName + `</a></html>`
		got, err := parseRemoteIndex(
			strings.NewReader(raw),
			"https://cdn.example/new/simple/",
			"demo",
		)
		if err != nil || len(got.files) != 1 || got.files[0].target != tc.target+distName {
			t.Fatalf("base resolution: %+v %v", got, err)
		}
	}
	for _, raw := range []string{
		`<html><base href="file:///tmp/"></html>`,
		`<html><base href="https://u:p@example.com/"></html>`,
		`<html><base href="/one/"><base href="/two/"></html>`,
		`<html><a href="` + distName + `">` + distName + `</a><base href="/two/"></html>`,
		`<html><base href="/one/#fragment"></html>`,
	} {
		if _, err := parseRemoteIndex(strings.NewReader(raw), indexBase, "demo"); err == nil {
			t.Fatalf("accepted unsafe base: %s", raw)
		}
	}
	if _, err := parseRemoteIndex(strings.NewReader(`<html></html>`), "", "demo"); err == nil {
		t.Fatal("missing response base accepted")
	}
}

func TestWarehouseAttributeSchemaAndVersionKeys(t *testing.T) {
	for _, field := range []string{"requires_python", "yanked_reason", "yanked"} {
		if validateWarehouseAttributes(remoteDoc{field: encode([]string{"invalid"})}) == nil {
			t.Fatalf("accepted malformed %s", field)
		}
	}
	if validateWarehouseAttributes(
		remoteDoc{
			"requires_python": encode(nil),
			"yanked_reason":   encode(nil),
			"yanked":          encode(false),
		},
	) != nil {
		t.Fatal("nullable Warehouse attributes rejected")
	}
	raw := `{"info":{"name":"demo","version":"1.0"},"urls":[],"releases":{"../bad":[]}}`
	if _, err := parseWarehouse(strings.NewReader(raw), indexBase, "demo", ""); err == nil {
		t.Fatal("invalid empty release key accepted")
	}
}

func TestRemoteHTMLRepositoryVersion(t *testing.T) {
	for _, decl := range []string{"", `<meta name="pypi:repository-version" content="1.0">`, `<meta name="PYPI:REPOSITORY-VERSION" content="1.9"><meta name="pypi:repository-version" content="1.9">`, `<meta name="other" content="2.0">`} {
		if _, err := parseRemoteIndex(
			strings.NewReader("<html>"+decl+"</html>"),
			indexBase,
			"demo",
		); err != nil {
			t.Fatal(decl, err)
		}
	}
	for _, decl := range []string{`<meta name="pypi:repository-version" content="2.0">`, `<meta name="pypi:repository-version">`, `<meta name="pypi:repository-version" content="1.x">`, `<meta name="pypi:repository-version" content="1.0"><meta name="pypi:repository-version" content="1.1">`} {
		if _, err := parseRemoteIndex(
			strings.NewReader("<html>"+decl+"</html>"),
			indexBase,
			"demo",
		); err == nil {
			t.Fatal("accepted", decl)
		}
	}
}

func TestRemoteSignatureAdvertisements(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		for _, representation := range []string{"json", "html"} {
			raw := `{"meta":{"api-version":"1.0"},"name":"demo","files":[{"filename":"` + distName + `","url":"` + distName + `","hashes":{},"gpg-sig":` + value + `}]}`
			if representation == "html" {
				raw = `<html><a href="` + distName + `" data-gpg-sig="` + value + `">` + distName + `</a></html>`
			}
			idx, err := parseRemoteIndex(strings.NewReader(raw), indexBase, "demo")
			if err != nil {
				t.Fatal(err)
			}
			for _, out := range [][]byte{remoteProjectJSON(idx, idx.files, "demo", "https://proxy", true), remoteHTML(idx.files, "https://proxy", true)} {
				other, err := parseRemoteIndex(bytes.NewReader(out), "https://proxy/", "demo")
				if err != nil || other.files[0].attrs["data-gpg-sig"] != value {
					t.Fatal(string(out), err)
				}
			}
		}
	}
	for _, raw := range []string{`null`, `"true"`, `{}`, `1`} {
		d := remoteDoc{
			"filename": encode(distName),
			"url":      encode(distName),
			"hashes":   encode(map[string]string{}),
			"gpg-sig":  json.RawMessage(raw),
		}
		if _, err := parseJSONFile(d, indexBase, "demo"); err == nil {
			t.Fatal("malformed gpg-sig", raw)
		}
	}
	if _, err := parseHTMLFile(
		indexBase,
		"demo",
		distName,
		map[string]string{"href": distName, "data-gpg-sig": "yes"},
	); err == nil {
		t.Fatal("malformed HTML signature flag")
	}
}
