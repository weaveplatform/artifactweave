package pkgroute_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
)

// TestUnitUpstreamMatrix (#1371): the matrix parameters of an escaped
// item path in the form 7.x appends them to a remote request URL
// (RemoteRepoBase.buildRequestMatrixParams, then HttpRepo.encodeIfNeeded,
// 7.161.26): every parameter of every segment in request order,
// ";key=value", "+" escaped to %2B on a mandatory key, ";key" for a blank
// value. A key and value are decoded as ParsePath decodes them, a value's
// "+" being a space, and escaped again; a comma stays, and a character that
// would move the rest of the value elsewhere is escaped. A parameter that
// names no key or does not decode is left out.
func TestUnitUpstreamMatrix(t *testing.T) {
	for _, tc := range []struct{ escaped, want string }{
		{"", ""},
		{"repo/a/b.jar", ""},
		{"repo/a/b.jar;x=1", ";x=1"},
		{"repo;k=v/a/b.jar;x=1;y=2", ";k=v;x=1;y=2"},
		{"repo/a;k=v/b.jar;x=1", ";k=v;x=1"},
		{"repo/a/;x=1", ";x=1"},
		{"repo/a/b.jar;x+=1", ";x%2B=1"},
		{"repo/a/b.jar;x%2B=1", ";x%2B=1"},
		{"repo/a/b.jar;x+", ";x%2B"},
		{"repo/a/b.jar;x=", ";x"},
		{"repo/a/b.jar;x", ";x"},
		{"repo/a/b.jar;x=%20", ";x"},
		{"repo/a/b.jar;x=a,b", ";x=a,b"},
		{"repo/a/b.jar;x=a+b", ";x=a%20b"},
		{"repo/a/b.jar;x=a%2Bb", ";x=a%2Bb"},
		{"repo/a/b.jar;x=a%2Fb%3Fc%23d%25e", ";x=a%2Fb%3Fc%23d%25e"},
		{"repo/a/b.jar;x=a%3Bb", ";x=a%3Bb"},
		{"repo/a/b.jar;k%3Bz=1", ";k%3Bz=1"},
		{"repo/a/b.jar;k%3Dz=1", ";k%3Dz=1"},
		{"repo/a/b.jar;x=a=b", ";x=a=b"},
		{"repo/a/b.jar;x=a:b@c&d$e!f*g'h(i)j~k-l_m.n", ";x=a:b@c&d$e!f*g'h(i)j~k-l_m.n"},
		{"repo/a/b.jar;x=%C3%A9", ";x=%C3%A9"},
		{`repo/a/b.jar;x=a%5C,b`, `;x=a%5C,b`},
		{"repo/a/b.jar;;x=1;", ";x=1"},
		{"repo/a/b.jar;=1;x=1", ";x=1"},
		{"repo/a/b.jar;x=%zz;y=1", ";y=1"},
		{"repo/a/b.jar;%zz=1;y=1", ";y=1"},
	} {
		if got := pkgroute.UpstreamMatrix(tc.escaped); got != tc.want {
			t.Errorf("UpstreamMatrix(%q) = %q, want %q", tc.escaped, got, tc.want)
		}
	}
}
