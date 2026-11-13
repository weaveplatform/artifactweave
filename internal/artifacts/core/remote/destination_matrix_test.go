package remote_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

// TestUnitCheckedMatrix (#1420): a matrix string passes when it is ";"-led
// and escaped throughout, so the %5C, %00, %0D and %0A that 7.x sends for a
// backslash, NUL, CR or LF in a value pass (HttpRepo.encodeIfNeeded, then
// URIUtil.encodeQuery, 7.161.26). It is refused when it carries a byte a
// path segment may not carry, a stray "%", or a decoded piece between "/",
// "\" or NUL that is "." or "..", or starts with one of them followed by
// ";", "?" or "#": an upstream that decodes before it resolves would read
// the first as a step, and one that also strips path parameters or splits
// off the query or fragment would read the second as one.
func TestUnitCheckedMatrix(t *testing.T) {
	for _, matrix := range []string{
		";x=1",
		";x=a%5Cb",
		";x=a%00b",
		";x=a%0D%0Ab",
		";x=1%0D%0AHost:%20evil",
		";x=..",
		";x=..%2F",
		";x=a..%5C..b",
		";x=..%00",
		";x=a%2Fb%3Fc%23d%20e",
		";x=..;y=..%3F;z=..%23",
		";x=a%2F..b;y=1",
		";x=a%2F.x%3Fq",
		";x=a%2F...%23f",
		";x%2B=1;y;z=",
		";x=%e2%82%ac",
	} {
		if err := remote.CheckedMatrix(matrix); err != nil {
			t.Errorf("CheckedMatrix(%q) = %v, want nil", matrix, err)
		}
	}
	for _, matrix := range []string{
		"",
		"x=1",
		`;x=a\b`,
		";x=a/b",
		";x=a?b",
		";x=a#b",
		";x=a b",
		";x=a\r\nb",
		";x=a\x00b",
		";x=%",
		";x=%2",
		";x=%zz",
		";x=%2F..",
		";x=%2F.",
		";x=%2F%2E%2E",
		";x=1%2F..%2F..%2Fsecret",
		";x=%5C..",
		";x=a%5C.%5Cb",
		";x=%00..",
		";x=a%00.",
		";x=1%2F..;y=1",
		";x=1%2F.;y=1",
		";x=1%2F..%3Bq",
		";x=1%2F..%3Fq",
		";x=1%2F.%3Fq",
		";x=1%2F..%23f",
		";x=1%5C..;y=1",
		";x=1%00..%3Fq",
	} {
		if err := remote.CheckedMatrix(matrix); !errors.Is(err, remote.ErrSecurity) {
			t.Errorf("CheckedMatrix(%q) = %v, want ErrSecurity", matrix, err)
		}
	}
}

// TestUnitCheckedRequestURLJudgesOnlyTheMatrix (#1420): CheckedRequestURL
// takes a request URL whose path ends with the matrix string it is given,
// escapes and all, where CheckedURL refuses the same URL. The repository
// URL and the item path before the matrix are still refused for a
// backslash, NUL, CR, LF or dot segment, escaped or not, and so is a URL
// that does not end with the matrix, as a redirect elsewhere would not.
func TestUnitCheckedRequestURLJudgesOnlyTheMatrix(t *testing.T) {
	const base = "https://up.example/repo/a/file.bin"
	for _, matrix := range []string{";x=a%5Cb", ";x=a%00b", ";x=a%0D%0Ab", ";x=1;y%2B=2"} {
		u, err := remote.CheckedRequestURL(base+matrix+"?q=1", matrix)
		if err != nil || u.String() != base+matrix+"?q=1" {
			t.Errorf("CheckedRequestURL(%q) = %v, %v; want the URL", base+matrix, u, err)
		}
	}
	for _, raw := range []string{base + ";x=a%5Cb", base + ";x=a%00b", base + ";x=a%0D%0Ab"} {
		if _, err := remote.CheckedURL(raw); !errors.Is(err, remote.ErrSecurity) {
			t.Errorf("CheckedURL(%q) = %v, want ErrSecurity", raw, err)
		}
	}
	const matrix = ";x=a%5Cb"
	for _, bad := range []string{
		`a\b`, "a%5Cb", "a%5cb", "a%00b", "a%0Db", "a%0Ab", "a\x00b", "a\rb", "a\nb",
		".", "..", "%2e", "%2E%2E", "a%2F..%2Fb",
	} {
		for _, raw := range []string{
			"https://up.example/" + bad + "/a/file.bin" + matrix,
			"https://up.example/repo/" + bad + "/file.bin" + matrix,
			"https://up.example/repo/a/" + bad + matrix,
		} {
			_, err := remote.CheckedRequestURL(raw, matrix)
			if !errors.Is(err, remote.ErrSecurity) {
				t.Errorf("CheckedRequestURL(%q) = %v, want ErrSecurity", raw, err)
			}
		}
	}
	for _, tc := range []struct{ raw, matrix string }{
		{base + ";x=a%5Cb", ";y=1"},
		{base + ";x=a%5Cb", "x=a%5Cb"},
		{base + ";x=%2F..", ";x=%2F.."},
		{base + ";x=%5C..", ";x=%5C.."},
		{"https://up.example/repo/a%5Cb;x=1", ";x=1"},
		{"https://up.example/repo/a%5Cb;x=1", ""},
		{"https://user@up.example/repo/a;x=1", ";x=1"},
		{"ftp://up.example/repo/a;x=1", ";x=1"},
		{"https://up.example/repo/a;x=1#f", ";x=1"},
	} {
		_, err := remote.CheckedRequestURL(tc.raw, tc.matrix)
		if !errors.Is(err, remote.ErrSecurity) {
			t.Errorf("CheckedRequestURL(%q, %q) = %v, want ErrSecurity", tc.raw, tc.matrix, err)
		}
	}
}
