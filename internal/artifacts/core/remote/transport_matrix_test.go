package remote_test

import (
	"net/http"
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
// query. Parameters that could end the path or start a query or fragment
// are left off, so a malformed value never moves the request elsewhere.
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
			matrix:   ";x=1/../../secret",
			wantPath: "a/file.bin",
		},
		{
			name:     "a question mark",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1?y=2",
			wantPath: "a/file.bin",
		},
		{
			name:     "a fragment",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1#y",
			wantPath: "a/file.bin",
		},
		{
			name:     "a backslash",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   `;x=1\y`,
			wantPath: "a/file.bin",
		},
		{
			name:     "a line break",
			resource: source.Resource{Path: "a/file.bin", Method: http.MethodGet},
			matrix:   ";x=1\r\nHost: evil",
			wantPath: "a/file.bin",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := remote.TransportRequest(
				source.Request{Resource: tc.resource, MatrixParams: tc.matrix},
				model.RemoteCacheEntry{},
			)
			if r.Path != tc.wantPath || r.Query != tc.wantQuery ||
				r.ExternalURL != tc.wantExternal || r.Method != tc.resource.Method {
				t.Errorf("TransportRequest = path %q, query %q, external %q, method %q; "+
					"want %q, %q, %q, %q", r.Path, r.Query, r.ExternalURL, r.Method,
					tc.wantPath, tc.wantQuery, tc.wantExternal, tc.resource.Method)
			}
		})
	}
}
