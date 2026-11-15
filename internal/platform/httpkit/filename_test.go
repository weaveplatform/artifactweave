package httpkit_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// EncodeQuery writes a name as 7.161.26's BasicHttpUtils.encodeQuery does:
// RFC 2396's unreserved and reserved characters stay, '@' among them as
// RTFACT-25859 shows it in filename*, and a space, '"', '%', '#', a control
// character and every byte outside ASCII are percent-encoded in upper-case
// hex (issue 1578).
func TestUnitEncodeQueryKeepsRFC2396Characters(t *testing.T) {
	for in, want := range map[string]string{
		"file.bin":                    "file.bin",
		"test@file.txt":               "test@file.txt",
		"pkg(1).zip":                  "pkg(1).zip",
		"a b+c;d'e~f!g*h,i$j&k=l.zip": "a%20b+c;d'e~f!g*h,i$j&k=l.zip",
		"q?/:x":                       "q?/:x",
		`"%#<>[]\^{|}` + "`":          "%22%25%23%3C%3E%5B%5D%5C%5E%7B%7C%7D%60",
		"naïve.whl":                   "na%C3%AFve.whl",
		"tab\tdel\x7f":                "tab%09del%7F",
	} {
		if got := httpkit.EncodeQuery(in); got != want {
			t.Errorf("EncodeQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

// EncodeUnreserved keeps RFC 3986's unreserved characters alone, as
// Spring's UriUtils.encode does for setHeaderForNonAscii's filename*.
func TestUnitEncodeUnreservedKeepsOnlyUnreserved(t *testing.T) {
	for in, want := range map[string]string{
		"aZ09-._~":     "aZ09-._~",
		"日本 a+b&c.zip": "%E6%97%A5%E6%9C%AC%20a%2Bb%26c.zip",
		"(x)@y!":       "%28x%29%40y%21",
	} {
		if got := httpkit.EncodeUnreserved(in); got != want {
			t.Errorf("EncodeUnreserved(%q) = %q, want %q", in, got, want)
		}
	}
}

// EscapeQuoted escapes only a quote and a backslash, and PrintableASCII
// holds for ' ' to '~' alone, as commons-lang's isAsciiPrintable does.
func TestUnitQuotedNameHelpers(t *testing.T) {
	if got := httpkit.EscapeQuoted(`a "b" \c d`); got != `a \"b\" \\c d` {
		t.Errorf("EscapeQuoted = %q", got)
	}
	for in, want := range map[string]bool{
		"": true, " ~plain-1.0.zip": true, "tab\t": false, "del\x7f": false, "naïve": false,
	} {
		if got := httpkit.PrintableASCII(in); got != want {
			t.Errorf("PrintableASCII(%q) = %v, want %v", in, got, want)
		}
	}
}
