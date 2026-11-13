package remote_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// TestUnitTransportRequestAppendsMatrixParameters (#1371): a request's
// matrix parameters follow the upstream path, after its last segment, as
// 7.x's HttpRepo.downloadResource and assembleRetrieveInfoUrl append
// buildRequestMatrixParams to the remote request URL (7.161.26), with the
// query after them. A path the format translated takes them too. An
// external URL names its own resource and takes neither them nor the
// query. A byte a path segment may not carry, such as one that would end
// the path or start a query or a fragment, is percent-encoded, so a value
// never moves the request elsewhere; an escape already made is kept and a
// stray "%" becomes %25. A backslash, NUL, CR or LF travels as %5C, %00,
// %0D or %0A, as 7.x's encodeIfNeeded sends it (#1420). A string that does
// not start with ";" would join the file name, and one whose decoded form
// holds a piece between "/", "\" or NUL that reads as a dot segment would
// be a step for an upstream that decodes before it resolves
// (CheckedMatrix): both are left off and the bare path is asked for. Request.Matrix names what was
// appended, so that the transport checks it by that rule.
func TestUnitTransportRequestAppendsMatrixParameters(t *testing.T) {
	for _, tc := range []struct {
		name                string
		resource            source.Resource
		matrix              string
		wantPath, wantQuery string
		wantExternal        string
	}{
		{
			name:     "no parameters",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			wantPath: "a/file.bin",
		},
		{
			name: "parameters and a query",
			resource: source.Resource{
				Path: "a/file.bin", Method: http.MethodGet, Query: "q=1",
			},
			matrix:    ";x=1;y%2B=2;z",
			wantPath:  "a/file.bin;x=1;y%2B=2;z",
			wantQuery: "q=1",
		},
		{
			name: "a HEAD",
			resource: source.Resource{
				Path: "a/file.bin", Method: http.MethodHead,
			},
			matrix:   ";x=1",
			wantPath: "a/file.bin;x=1",
		},
		{
			name: "an escaped path",
			resource: source.Resource{
				Path: "a/b c.bin", EscapedPath: "a/b%20c.bin", Method: http.MethodGet,
			},
			matrix:   ";x=1",
			wantPath: "a/b%20c.bin;x=1",
		},
		{
			name: "a translated path",
			resource: source.Resource{
				Path: "a/file.bin", UpstreamPath: "/b/file.bin", Method: http.MethodGet,
			},
			matrix:   ";x=1",
			wantPath: "/b/file.bin;x=1",
		},
		{
			name: "an external URL",
			resource: source.Resource{
				Path: "a/file.bin", Method: http.MethodGet, Query: "q=1",
				ExternalURL: "https://other.example/file.bin",
			},
			matrix:       ";x=1",
			wantExternal: "https://other.example/file.bin",
		},
		{
			name:     "parameters without a leading semicolon",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   "x=1",
			wantPath: "a/file.bin",
		},
		{
			name:     "a slash",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1/2",
			wantPath: "a/file.bin;x=1%2F2",
		},
		{
			name:     "a question mark",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1?y=2",
			wantPath: "a/file.bin;x=1%3Fy=2",
		},
		{
			name:     "a fragment",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1#y",
			wantPath: "a/file.bin;x=1%23y",
		},
		{
			name:     "a backslash",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   `;x=1\y`,
			wantPath: "a/file.bin;x=1%5Cy",
		},
		{
			name:     "a line break",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1\r\nHost: evil",
			wantPath: "a/file.bin;x=1%0D%0AHost:%20evil",
		},
		{
			name:     "an escaped line break",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%0D%0A",
			wantPath: "a/file.bin;x=1%0D%0A",
		},
		{
			name:     "an escaped backslash",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%5Cy",
			wantPath: "a/file.bin;x=1%5Cy",
		},
		{
			name:     "a NUL",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%00",
			wantPath: "a/file.bin;x=1%00",
		},
		{
			name:     "dots that are not a segment",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=..;y=a..%5C..b;z=..%00",
			wantPath: "a/file.bin;x=..;y=a..%5C..b;z=..%00",
		},
		{
			name:     "a dot segment between backslashes",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%5C..%5Csecret",
			wantPath: "a/file.bin",
		},
		{
			name:     "a dot segment after a NUL",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%00..",
			wantPath: "a/file.bin",
		},
		{
			name:     "a dot segment before a parameter",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%2F..;y=1",
			wantPath: "a/file.bin",
		},
		{
			name:     "a dot segment before a query",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%2F..%3Fq",
			wantPath: "a/file.bin",
		},
		{
			name:     "a single dot after a backslash",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%5C.",
			wantPath: "a/file.bin",
		},
		{
			name:     "a dot segment",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1/../../secret",
			wantPath: "a/file.bin",
		},
		{
			name:     "an escaped dot segment",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1%2F..",
			wantPath: "a/file.bin",
		},
		{
			name:     "a space",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=a b",
			wantPath: "a/file.bin;x=a%20b",
		},
		{
			name:     "a stray percent sign",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=100%;y=%2;z=%zz",
			wantPath: "a/file.bin;x=100%25;y=%252;z=%25zz",
		},
		{
			name:     "escapes already made",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=a%2Fb%3Fc%23d;y=%e2%82%ac;z%2B=1",
			wantPath: "a/file.bin;x=a%2Fb%3Fc%23d;y=%e2%82%ac;z%2B=1",
		},
		{
			name:     "bytes outside ASCII",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=\u20ac",
			wantPath: "a/file.bin;x=%E2%82%AC",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := remote.TransportRequest(
				source.Request{Resource: tc.resource, MatrixParams: tc.matrix},
				model.RemoteCacheEntry{},
			)
			wantMatrix := strings.TrimPrefix(tc.wantPath, tc.resource.Path)
			if tc.resource.UpstreamPath != "" {
				wantMatrix = strings.TrimPrefix(tc.wantPath, tc.resource.UpstreamPath)
			} else if tc.resource.EscapedPath != "" {
				wantMatrix = strings.TrimPrefix(tc.wantPath, tc.resource.EscapedPath)
			}
			if r.Matrix != wantMatrix {
				t.Errorf("TransportRequest Matrix = %q, want %q", r.Matrix, wantMatrix)
			}
			if r.Path != tc.wantPath || r.Query != tc.wantQuery ||
				r.ExternalURL != tc.wantExternal || r.Method != tc.resource.Method {
				t.Errorf("TransportRequest = path %q, query %q, external %q, method %q; "+
					"want %q, %q, %q, %q", r.Path, r.Query, r.ExternalURL, r.Method,
					tc.wantPath, tc.wantQuery, tc.wantExternal, tc.resource.Method)
			}
		})
	}
}
