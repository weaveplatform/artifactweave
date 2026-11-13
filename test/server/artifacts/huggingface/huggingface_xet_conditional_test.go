package huggingface_test

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
)

// TestAccXetChunkRangeAnswersConditionalsAsTheDownload: a Xet chunk range,
// which the CAS route encodes from the stored xorb on each request with the
// time of the request as its modified time, answers If-Match and
// If-Unmodified-Since as the plain request, and a GET's Range with the part
// as before (#1438). The route takes no HEAD.
func TestAccXetChunkRangeAnswersConditionalsAsTheDownload(t *testing.T) {
	w := newXetWorld(t)
	publishXetFixture(t, w, "hf")
	chunks := xetChunkURL(t, w, xetTokenFor(t, w, "hf", "acme/demo", "read", "reader"))
	kittest.WantDownloadConditionals(t, "Xet chunk range",
		func(method string, headers ...string) *httptest.ResponseRecorder {
			return w.do(method, chunks, "", "", headers...)
		}, false)
}
