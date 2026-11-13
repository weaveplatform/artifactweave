package nix_test

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
)

// TestAccNixCacheInfoAnswersConditionalsAsTheDownload: nix-cache-info,
// which the route writes itself, answers If-Match and If-Unmodified-Since
// as the plain request and a HEAD's Range with the whole file, as 7.x's
// download answers a file (#1438, #1362).
func TestAccNixCacheInfoAnswersConditionalsAsTheDownload(t *testing.T) {
	w := hostedWorld(t)
	path := native + "nix-cache-info"
	kittest.WantDownloadConditionals(t, path,
		func(method string, headers ...string) *httptest.ResponseRecorder {
			return w.Do(method, path, "admin", "", headers...)
		}, true)
}
