package acceptance

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerRemote(sc *godog.ScenarioContext) {
	w.registerSmartRemote(sc)
	w.registerRemoteNPM(sc)
	w.registerRemoteHelm(sc)
	sc.Step(`^a generic upstream serves "([^"]*)" with content "([^"]*)"$`, w.remoteUpstream)
	sc.Step(`^the generic upstream stops$`, func() { w.remote.Close() })
	sc.Step(
		`^the generic upstream received (\d+) (GET|HEAD) requests for "([^"]*)"$`,
		w.remoteRequests,
	)
	sc.Step(`^I run curl "([^"]*)"$`, w.runCurl)
}

func (w *world) remoteUpstream(path, body string) error {
	if w.remote == nil {
		w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
		w.closers = append(w.closers, w.remote.Close)
		if w.vars == nil {
			w.vars = map[string]string{}
		}
		w.vars["upstream"] = w.remote.URL
	}
	w.remote.Set(path, fakeupstream.Resource{Body: []byte(body)})
	return nil
}

func (w *world) remoteRequests(want int, method, path string) error {
	if got := w.remote.Count(method, path); got != want {
		return fmt.Errorf("upstream %s %s count %d, want %d", method, path, got, want)
	}
	return nil
}

func (w *world) runCurl(args string) error {
	if _, err := w.filesDir(); err != nil {
		return err
	}
	cmd := exec.Command("curl", strings.Fields(w.expand(args))...)
	cmd.Dir = w.dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	w.body, w.lastRC = out, 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			w.lastRC = exit.ExitCode()
			return nil
		}
		return err
	}
	return nil
}
