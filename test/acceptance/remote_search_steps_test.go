package acceptance

import (
	"net/http"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerRemoteSearch(sc *godog.ScenarioContext) {
	sc.Step(
		`^an upstream search document "([^"]*)" of type "([^"]*)" contains:$`,
		w.remoteSearchDocument,
	)
}

func (w *world) remoteSearchDocument(path, media string, document *godog.DocString) error {
	if err := w.remoteUpstream(path, document.Content); err != nil {
		return err
	}
	w.remote.Set(
		path,
		fakeupstream.Resource{
			Body:   []byte(document.Content),
			Header: http.Header{"Content-Type": {media}},
		},
	)
	return nil
}
