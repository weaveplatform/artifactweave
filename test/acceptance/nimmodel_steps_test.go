package acceptance

import (
	"net/http"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerNIM(sc *godog.ScenarioContext) {
	sc.Step(`^an NGC fixture serves NIM metadata for the matching image$`, w.nimMetadata)
}

func (w *world) nimMetadata() error {
	if err := w.remoteUpstream("/v2/nim/metadata", "{}"); err != nil {
		return err
	}
	w.remote.Set("/v2/nim/metadata", fakeupstream.Resource{
		Body: []byte(
			`{"release_details":{"container_version":"1.3"},"profiles":[{"profile_id":"h100","tested_gpu_devices":[]}]}`,
		),
		Header: http.Header{"Content-Type": {"application/json"}},
	})
	return nil
}
