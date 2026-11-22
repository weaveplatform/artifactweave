package source_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitSelectedContentRequiresCompleteFileFacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*source.Request)
	}{
		{"negative size", func(q *source.Request) { q.Resource.SelectedContent.Size = -1 }},
		{"missing digests", func(q *source.Request) { q.Resource.SelectedContent.Checksums = kernel.Checksums{} }},
		{"malformed digest", func(q *source.Request) { q.Resource.SelectedContent.Checksums.SHA256 = strings.Repeat("g", 64) }},
		{"short digest", func(q *source.Request) { q.Resource.SelectedContent.Checksums.SHA256 = "abc" }},
		{"native digest conflict", func(q *source.Request) { q.Resource.Checksums.SHA256 = strings.Repeat("b", 64) }},
		{"cache-only", func(q *source.Request) { q.CacheOnly = true }},
		{"head", func(q *source.Request) { q.Resource.Method = http.MethodHead }},
		{"version query", func(q *source.Request) { q.Resource.Kind = source.ResourceKindVersions }},
		{"discovery metadata", func(q *source.Request) { q.Resource.Kind = source.ResourceKindMetadata }},
		{"transient control document", func(q *source.Request) { q.Resource.Transient = true }},
		{"folder authority", func(q *source.Request) { q.Resource.PermissionFolder = true }},
		{"root discovery authority", func(q *source.Request) { q.Resource.PermissionRoot = true }},
		{"memory-only metadata", func(q *source.Request) { q.Resource.NoCacheFile = true }},
		{"checksum companion", func(q *source.Request) { q.Resource.ChecksumOnly = true }},
		{"partial body", func(q *source.Request) { q.Resource.Range = "bytes=1-" }},
		{"conditional body", func(q *source.Request) { q.Resource.IfNoneMatch = `"old"` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := request(t, "")
			q.Resource.SelectedContent = &source.ContentExpectation{Size: 7, Checksums: kernel.Checksums{SHA256: strings.Repeat("a", 64)}}
			tc.mutate(&q)
			assertRequestRejectedBeforeResolution(t, q)
		})
	}
	for _, tc := range []struct {
		name string
		kind source.ResourceKind
	}{
		{"ordinary artifact", source.ResourceKindArtifact},
		{"addressed metadata file", source.ResourceKindMetadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := request(t, "")
			q.Resource.Kind, q.Resource.RepositoryFile = tc.kind, true
			q.Resource.SelectedContent = &source.ContentExpectation{Size: 0, Checksums: kernel.Checksums{SHA256: strings.Repeat("a", 64)}}
			q.Resource.Checksums.SHA256 = strings.Repeat("A", 64)
			svc := source.Service{Resolve: identity}
			if _, err := svc.Authorize(t.Context(), q); err != nil {
				t.Fatal("selected complete file required another representation variant", err)
			}
		})
	}
}
