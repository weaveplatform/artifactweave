package cocoapods

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
)

func TestUnitCachedHTTPSourceDoesNotUseHostedArchiveGrammar(t *testing.T) {
	path := "HelloPod/1.0.0/source.tar.gz"
	h := Handler{}
	if _, err := h.Admit(t.Context(), format.Env{}, repo.Config{PackageType: repo.CocoaPods},
		format.Write{Path: path, Cached: true}); err != nil {
		t.Fatalf("CDN-bound remote source rejected by hosted upload grammar: %v", err)
	}
	if _, err := h.Admit(t.Context(), format.Env{}, repo.Config{PackageType: repo.CocoaPods},
		format.Write{Path: path}); err == nil {
		t.Fatal("user upload to remote source cache path admitted")
	}
}
