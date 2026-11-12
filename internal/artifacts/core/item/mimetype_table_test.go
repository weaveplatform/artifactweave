package item

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// TestUnitMimeTypeTable: every extension of 7.x's default mimetypes.xml
// (version 17, the 7.161.26 OSS sources) answers its row's type, whatever
// its case (#1315). The expected types are copied from the XML, not from
// mimeTypes, so a row typed by hand differently fails here.
func TestUnitMimeTypeTable(t *testing.T) {
	rows := map[string]string{
		"txt, properties, mf, asc, log, yml, yaml, tf, md": "text/plain",
		"htm, html":                         "text/html",
		"css":                               "text/css",
		"xsl":                               "text/xsl",
		"xslt":                              "text/xslt",
		"java":                              "text/x-java-source",
		"fx":                                "text/x-javafx-source",
		"groovy, gradle":                    "text/x-groovy-source",
		"h, c, cc, cpp":                     "text/x-c",
		"dtd":                               "application/xml-dtd",
		"xsd":                               "application/xml-schema",
		"ent":                               "application/xml-external-parsed-entity",
		"xhtml":                             "application/xhtml+xml",
		"json":                              "application/json",
		"py":                                "text/x-python",
		"jardiff":                           "application/x-java-archive-diff",
		"zip":                               "application/zip",
		"xz":                                "application/x-xz",
		"tar":                               "application/x-tar",
		"tgz, gz":                           "application/x-gzip",
		"bz2":                               "application/x-bzip2",
		"7z":                                "application/x-7z-compressed",
		"nupkg":                             "application/x-nupkg",
		"conda":                             "application/x-conda",
		"rar":                               "application/x-rar-compressed",
		"apk":                               "application/vnd.android.package-archive",
		"rpm":                               "application/x-rpm",
		"gem":                               "application/x-rubygems",
		"rz":                                "application/x-ruby-marshal",
		"deb, ddeb":                         "application/x-debian-package",
		"box":                               "application/x-vagrant-box",
		"info":                              "application/json+info",
		"mod":                               "text/plain+mod",
		"swift":                             "text/x-swift",
		"scala":                             "text/x-scala-source",
		"rb":                                "text/x-ruby-source",
		"sh":                                "text/x-script.sh",
		"cs":                                "text/x-csharp.sh",
		"xml, xsi":                          "application/xml",
		"pom":                               "application/x-maven-pom+xml",
		"ivy":                               "application/x-ivy+xml",
		"nuspec":                            "application/x-nuspec+xml",
		"jnlp":                              "application/x-java-jnlp-file",
		"sha1, sha256, md5":                 "application/x-checksum",
		"jar, war, ear, sar, har, hpi, jpi": "application/java-archive",
	}
	count := 0
	for exts, want := range rows {
		for ext := range strings.SplitSeq(exts, ", ") {
			count++
			for _, name := range []string{"f." + ext, "dir/F." + strings.ToUpper(ext)} {
				if got := MimeType(name); got != want {
					t.Errorf("MimeType(%q) = %q, want %q", name, got, want)
				}
			}
		}
	}
	// 69 extensions: the 74 of version 17, less the five two-part ones
	// below, which the lookup never reaches.
	if count != 69 || len(mimeTypes) != count {
		t.Fatalf("%d extensions tested, %d in the table, want 69", count, len(mimeTypes))
	}
}

// TestUnitMimeTypeLastExtension: 7.x types a name by its last extension
// (NamingUtils.getMimeType over PathUtils.getExtension), so the table's
// two-part rows never match and a name with no known last extension is
// application/octet-stream.
func TestUnitMimeTypeLastExtension(t *testing.T) {
	for name, want := range map[string]string{
		"a.tar.gz":        "application/x-gzip",
		"a.tar.bz2":       "application/x-bzip2",
		"a.tar.xz":        "application/x-xz",
		"a.nar.xz":        "application/x-xz",
		"a.jar.pack.gz":   "application/x-gzip",
		"a.jar.sha512":    "application/octet-stream",
		"a.yaml.bak":      "application/octet-stream",
		"README":          "application/octet-stream",
		"conf.d/settings": "application/octet-stream",
		"dir.json/file":   "application/octet-stream",
		"Package.swift":   "text/x-swift",
		"index.yaml":      "text/plain",
	} {
		if got := MimeType(name); got != want {
			t.Errorf("MimeType(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestUnitRemoteMIMEBlockList: a remote refuses an upstream's label only
// when it differs from the path's type and is on the block list, which is
// text/html and application/xhtml+xml unless the repository's
// mismatchingMimeTypesOverrideList replaces it (7.x
// HttpRepo.blockMismatchingMimeTypes, #1316). The body is not read for its
// type, and the cached file keeps its path's type whatever its label.
func TestUnitRemoteMIMEBlockList(t *testing.T) {
	const html = "<!DOCTYPE html><html>login</html>"
	for _, tc := range []struct {
		name, path, label, override, body string
		refused                           bool
	}{
		{"jar-java-archive-alias", "lib/a.jar", "application/x-java-archive", "", "jar bytes", false},
		{"box-binary-octet-stream", "boxes/focal.box", "binary/octet-stream", "", "box bytes", false},
		{"tar-gtar", "a.tar", "application/x-gtar", "", "tar bytes", false},
		{"yaml-text-yaml", "index.yaml", "text/yaml", "", "a: b", false},
		{"rpm-rpm-label", "a.rpm", "application/x-redhat-package-manager", "", "rpm bytes", false},
		{"json-png", "a.json", "image/png", "", "{}", false},
		{"malformed-label", "a.jar", "invalid;", "", "jar bytes", false},
		{"wildcard-label", "a.jar", "*/*", "", "jar bytes", false},
		{"html-body-octet-stream", "a.jar", "application/octet-stream", "", html, false},
		{"html-body-own-label", "a.jar", "application/java-archive", "", html, false},
		{"html-body-no-label", "a.jar", "", "", html, false},
		{"html-label-untyped-path", "a.bin", "text/html", "", html, false},
		{"html-label-html-path", "a.htm", "text/html; charset=utf-8", "", html, false},
		{"html-label", "a.jar", "text/html", "", html, true},
		{"html-label-parameters", "a.yaml", "TEXT/HTML ; charset=utf-8", "", html, true},
		{"xhtml-label", "a.deb", "application/xhtml+xml", "", html, true},
		{"override-blocks-its-types", "a.jar", "application/x-other", " Application/X-Other ;q=1, a/b", "x", true},
		{"override-replaces-default", "a.jar", "text/html", "application/x-other", html, false},
		{"blank-override-keeps-default", "a.jar", "text/html", "  ", html, true},
		{"empty-entries-block-nothing", "a.jar", "text/html", " , ", html, false},
		{"override-type-matching-path", "a.jar", "application/java-archive", "application/java-archive", "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.Token.Key.Path = tc.path
			u.Repo.MismatchingMimeTypesOverrideList = tc.override
			u.ContentType = tc.label
			u.Body, u.Length = strings.NewReader(tc.body), int64(len(tc.body))
			var err error
			u.Token, err = w.db.RemoteCache().Begin(t.Context(), u.Token.Key)
			if err != nil {
				t.Fatal(err)
			}
			result, err := w.svc.Cache(t.Context(), u)
			if tc.refused {
				if !errors.Is(err, ErrRemoteMIME) {
					t.Fatalf("label %q: %v, want %v", tc.label, err, ErrRemoteMIME)
				}
				if _, err = w.db.RemoteCache().Read(t.Context(), u.Token.Key); err == nil {
					t.Fatal("refused body cached")
				}
				return
			}
			if err != nil {
				t.Fatalf("label %q refused: %v", tc.label, err)
			}
			if result.Body != nil {
				_ = result.Body.Close()
			}
			if want := MimeType(tc.path); result.Item.MimeType != want {
				t.Fatalf("recorded as %q, want %q", result.Item.MimeType, want)
			}
		})
	}
}

// TestUnitRemoteMIMEBlockingOff: with blockMismatchingMimeTypes off a
// remote caches a blocked label too.
func TestUnitRemoteMIMEBlockingOff(t *testing.T) {
	w, u := remoteWorld(t)
	off := false
	u.Repo.BlockMismatchingMimeTypes = &off
	u.Token.Key.Path = "a.jar"
	u.ContentType, u.Body, u.Length = "text/html", strings.NewReader("<html/>"), 7
	var err error
	u.Token, err = w.db.RemoteCache().Begin(t.Context(), u.Token.Key)
	if err != nil {
		t.Fatal(err)
	}
	result, err := w.svc.Cache(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	if result.Body != nil {
		_ = result.Body.Close()
	}
}

// TestUnitRemoteMetadataMIMEOverrideList: raw metadata an adapter expects
// as JSON is refused under a label the override list names, which the
// list once let through, and still cached under a JSON label it does not
// name.
func TestUnitRemoteMetadataMIMEOverrideList(t *testing.T) {
	for _, tc := range []struct {
		name, override string
		refused        bool
	}{
		{"listed", "application/vnd.npm.install-v1+json", true},
		{"listed-text-plain", "text/plain", true},
		{"unlisted", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.Kind = model.RemoteMetadata
			u.ExpectedContentType = "application/json"
			u.Repo.MismatchingMimeTypesOverrideList = tc.override
			label := "application/vnd.npm.install-v1+json"
			if tc.name == "listed-text-plain" {
				label = "text/plain"
			}
			data := []byte(`{"name":"demo"}`)
			u.ContentType, u.Body, u.Length = label, bytes.NewReader(data), int64(len(data))
			result, err := w.svc.Cache(t.Context(), u)
			if tc.refused {
				if !errors.Is(err, ErrRemoteMIME) {
					t.Fatalf("%v, want %v", err, ErrRemoteMIME)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Body != nil {
				_ = result.Body.Close()
			}
		})
	}
}
