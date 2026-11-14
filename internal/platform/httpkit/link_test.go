package httpkit_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// The next page's target keeps the client's path and every parameter as
// sent, in its order, and changes only the position.
func TestUnitNextLink(t *testing.T) {
	const path = "/artifactory/api/repositories"
	for _, c := range []struct{ query, want string }{
		{"limit=2", "</artifactory/api/repositories?limit=2&offset=7>; rel=\"next\""},
		{"limit=2&offset=5", "</artifactory/api/repositories?limit=2&offset=7>; rel=\"next\""},
		{"offset=5&type=local&limit=2", "</artifactory/api/repositories?offset=7&type=local&limit=2>; rel=\"next\""},
		// Parameters it does not know, and their encoding, stay as sent.
		{"limit=2&x=a%20b&flag&packageType=Maven", "</artifactory/api/repositories?limit=2&x=a%20b&flag&packageType=Maven&offset=7>; rel=\"next\""},
		// An escaped name is still the position; repeats collapse to one.
		{"of%66set=1&limit=2&offset=3", "</artifactory/api/repositories?offset=7&limit=2>; rel=\"next\""},
		// Empty pieces go; bytes a URI cannot hold are escaped.
		{"&limit=2&&q=<a b>", "</artifactory/api/repositories?limit=2&q=%3Ca%20b%3E&offset=7>; rel=\"next\""},
		{"", "</artifactory/api/repositories?offset=7>; rel=\"next\""},
		// A name that does not unescape is kept as sent, escaped.
		{"%zz=1&limit=2", "</artifactory/api/repositories?%zz=1&limit=2&offset=7>; rel=\"next\""},
		{"q=\x7f\xff", "</artifactory/api/repositories?q=%7F%FF&offset=7>; rel=\"next\""},
	} {
		if got := httpkit.NextLink(path, c.query, "offset", "7"); got != c.want {
			t.Errorf("NextLink(%q) = %s, want %s", c.query, got, c.want)
		}
	}
	if got := httpkit.NextLink(
		"/a b",
		"",
		"cursor",
		"x y",
	); got != "</a%20b?cursor=x+y>; rel=\"next\"" {
		t.Errorf("escaped path and value: %s", got)
	}
}
