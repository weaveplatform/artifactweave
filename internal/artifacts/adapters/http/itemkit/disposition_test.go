package itemkit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// The package download service names a file inside the quotes as it is,
// spaces and sub-delimiters included, with no filename* part, as
// 7.161.26's JerseyArtifactoryResponse writes it. A quote or a backslash,
// which 7.x's PathValidator refuses in a path, is escaped so the name
// cannot end the quoted string early. A name with a character outside
// printable ASCII, a control character included, is named by filename*
// alone, every byte but RFC 3986's unreserved characters percent-encoded,
// as RequestResponseHelper.setHeaderForNonAscii names it. The item route
// keeps ItemDisposition's form with filename* (#1505).
func TestUnitPackageDispositionQuotesTheNameAsItIs(t *testing.T) {
	for name, want := range map[string]string{
		"rake-12.3.1.gem":   `attachment; filename="rake-12.3.1.gem"`,
		"release one.box":   `attachment; filename="release one.box"`,
		"pkg+1.0;x=(1).zip": `attachment; filename="pkg+1.0;x=(1).zip"`,
		"~tilde_ok-1.0.bin": `attachment; filename="~tilde_ok-1.0.bin"`,
		`odd"name.bin`:      `attachment; filename="odd\"name.bin"`,
		`back\slash.bin`:    `attachment; filename="back\\slash.bin"`,
		"naïve-1.0.whl":     "attachment; filename*=UTF-8''na%C3%AFve-1.0.whl",
		"日本 a+b&c~d.zip":    "attachment; filename*=UTF-8''%E6%97%A5%E6%9C%AC%20a%2Bb%26c~d.zip",
		"tab\tname.bin":     "attachment; filename*=UTF-8''tab%09name.bin",
		"del\x7fname.bin":   "attachment; filename*=UTF-8''del%7Fname.bin",
	} {
		if got := itemkit.PackageDisposition(name); got != want {
			t.Errorf("PackageDisposition(%q) = %q, want %q", name, got, want)
		}
	}
	if got, want := itemkit.ItemDisposition("release one.box"),
		`attachment; filename="release one.box"; filename*=UTF-8''release%20one.box`; got != want {
		t.Errorf("ItemDisposition = %q, want %q", got, want)
	}
}

// The item route quotes the name as it is and query-encodes filename* as
// 7.161.26's HttpArtifactoryResponse does (BasicHttpUtils.encodeQuery):
// RFC 2396's unreserved and reserved characters stay, '+', '(', ';', '@'
// and '&' among them, and a space, '%' or '"' is encoded. A name outside
// printable ASCII is named by filename* alone, as setHeaderForNonAscii
// names it (#1578). X-Artifactory-Filename is the query-encoded name.
func TestUnitItemDispositionQueryEncodesFilenameStar(t *testing.T) {
	for name, want := range map[string][2]string{
		"lib-1.0.jar": {
			`attachment; filename="lib-1.0.jar"; filename*=UTF-8''lib-1.0.jar`, "lib-1.0.jar",
		},
		"a b+c(1);d'e@f~g!h,i$j&k=l.zip": {
			`attachment; filename="a b+c(1);d'e@f~g!h,i$j&k=l.zip"; ` +
				`filename*=UTF-8''a%20b+c(1);d'e@f~g!h,i$j&k=l.zip`,
			"a%20b+c(1);d'e@f~g!h,i$j&k=l.zip",
		},
		"100%.txt": {
			`attachment; filename="100%.txt"; filename*=UTF-8''100%25.txt`, "100%25.txt",
		},
		`odd"name.bin`: {
			`attachment; filename="odd\"name.bin"; filename*=UTF-8''odd%22name.bin`, "odd%22name.bin",
		},
		"naïve 1.0.zip": {
			"attachment; filename*=UTF-8''na%C3%AFve%201.0.zip", "na%C3%AFve%201.0.zip",
		},
		"a+b.zip\u00e9": {
			"attachment; filename*=UTF-8''a%2Bb.zip%C3%A9", "a+b.zip%C3%A9",
		},
	} {
		if got := itemkit.ItemDisposition(name); got != want[0] {
			t.Errorf("ItemDisposition(%q) = %q, want %q", name, got, want[0])
		}
		if got := itemkit.FilenameHeader(name); got != want[1] {
			t.Errorf("FilenameHeader(%q) = %q, want %q", name, got, want[1])
		}
	}
}

// A repository allows content browsing when it is local, federated or
// remote and sets archiveBrowsingEnabled; a virtual repository never does,
// as 7.161.26's contentBrowsingDisabled reads only a real repository's
// descriptor (#1579). On one that does, a file is named by no
// Content-Disposition, or inline by filename* alone when its name is
// outside printable ASCII; elsewhere by the form given.
func TestUnitFileDispositionFollowsContentBrowsing(t *testing.T) {
	on, off := true, false
	for _, c := range []struct {
		config repo.Config
		want   bool
	}{
		{repo.Config{Class: repo.Local, ArchiveBrowsingEnabled: &on}, true},
		{repo.Config{Class: repo.Federated, ArchiveBrowsingEnabled: &on}, true},
		{repo.Config{Class: repo.Remote, ArchiveBrowsingEnabled: &on}, true},
		{repo.Config{Class: repo.Virtual, ArchiveBrowsingEnabled: &on}, false},
		{repo.Config{Class: repo.Local, ArchiveBrowsingEnabled: &off}, false},
		{repo.Config{Class: repo.Local}, false},
	} {
		if got := itemkit.Browsable(c.config); got != c.want {
			t.Errorf("Browsable(%s, %v) = %t", c.config.Class, c.config.ArchiveBrowsingEnabled, got)
		}
	}
	browsing := repo.Config{Class: repo.Local, ArchiveBrowsingEnabled: &on}
	plain := repo.Config{Class: repo.Local}
	for _, c := range []struct {
		config     repo.Config
		name, want string
	}{
		{plain, "a b.zip", `attachment; filename="a b.zip"`},
		{plain, "naïve.zip", "attachment; filename*=UTF-8''na%C3%AFve.zip"},
		{browsing, "a b.zip", ""},
		{browsing, "naïve.zip", "inline; filename*=UTF-8''na%C3%AFve.zip"},
	} {
		if got := itemkit.FileDisposition(
			c.config,
			c.name,
			itemkit.PackageDisposition,
		); got != c.want {
			t.Errorf(
				"FileDisposition(%t, %q) = %q, want %q",
				itemkit.Browsable(c.config),
				c.name,
				got,
				c.want,
			)
		}
	}
}

// StreamNames names a remote's streamed file as serveFile names the copy:
// X-Artifactory-Filename always, the Content-Disposition by the content
// browsing flag in the form given, the 304's form on a 304, and a
// browser's view of a GET from a browsable repository; a
// Content-Disposition a protocol set is kept (#1594).
func TestUnitStreamNamesNamesAsTheKeptCopy(t *testing.T) {
	on := true
	browsing := repo.Config{Class: repo.Remote, ArchiveBrowsingEnabled: &on}
	plain := repo.Config{Class: repo.Remote}
	const browser = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0"
	for _, c := range []struct {
		label                 string
		config                repo.Config
		name                  string
		form                  func(string) string
		agent, own            string
		notModified           bool
		filename, disposition string
		csp                   string
	}{
		{
			"item form", plain, "a b.jar", itemkit.ItemDisposition, "", "", false,
			"a%20b.jar", `attachment; filename="a b.jar"; filename*=UTF-8''a%20b.jar`, "",
		},
		{
			"package form", plain, "a b.whl", itemkit.PackageDisposition, "", "", false,
			"a%20b.whl", `attachment; filename="a b.whl"`, "",
		},
		{
			"own disposition", plain, "a.jar", itemkit.ItemDisposition, "", "inline", false,
			"a.jar", "inline", "",
		},
		{"browsable", browsing, "a.jar", itemkit.ItemDisposition, "", "", false, "a.jar", "", ""},
		{
			"browsable non-ASCII", browsing, "ï.jar", itemkit.ItemDisposition, "", "", false,
			"%C3%AF.jar", "inline; filename*=UTF-8''%C3%AF.jar", "",
		},
		{
			"browsable non-ASCII 304", browsing, "ï.jar", itemkit.ItemDisposition, "", "", true,
			"%C3%AF.jar", "", "",
		},
		{"browser", browsing, "lib.pom", itemkit.ItemDisposition, browser, "", false, "lib.pom", "", "sandbox"},
		{"browser 304", browsing, "lib.pom", itemkit.ItemDisposition, browser, "", true, "lib.pom", "", ""},
		{
			"browser, no flag", plain, "lib.pom", itemkit.ItemDisposition, browser, "", false,
			"lib.pom", `attachment; filename="lib.pom"; filename*=UTF-8''lib.pom`, "",
		},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("User-Agent", c.agent)
		h := http.Header{}
		if c.own != "" {
			h.Set("Content-Disposition", c.own)
		}
		itemkit.StreamNames(c.config, c.name, c.form)(h, r, c.notModified)
		if h.Get("X-Artifactory-Filename") != c.filename ||
			h.Get("Content-Disposition") != c.disposition ||
			h.Get("Content-Security-Policy") != c.csp {
			t.Errorf("%s: %v", c.label, h)
		}
	}
}
