package acceptance

import (
	"net/http"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) remoteDirectoryIndex(path string, document *godog.DocString) error {
	if err := w.remoteUpstream(path, document.Content); err != nil {
		return err
	}
	w.remote.Set(
		path,
		fakeupstream.Resource{
			Body:   []byte(document.Content),
			Header: http.Header{"Content-Type": {"text/html"}},
		},
	)
	return nil
}
