package remote

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
)

// A policy built without the CocoaPods slice's hook must keep answering a
// Git checkout as unsupported, as a policy without a pool always has,
// rather than falling through to an HTTP fetch of the Git URL.
func TestUnitGitFetchUnsetAnswersUnsupported(t *testing.T) {
	q := source.Request{Resource: source.Resource{
		CocoaPodsGit: &source.CocoaPodsGitSource{URL: "https://git.example/pod.git"},
	}}
	res, err := (&Policy{}).fetchResponse(t.Context(), q, source.Permit{}, Options{}, Request{})
	if res != nil {
		_ = res.Body.Close()
	}
	if !errors.Is(err, source.ErrUnsupported) {
		t.Fatal(err)
	}
}
