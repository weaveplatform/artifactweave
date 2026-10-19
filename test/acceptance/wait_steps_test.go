package acceptance

import (
	"time"

	"github.com/cucumber/godog"
)

// registerWaits adds the steps that wait for background work, such as
// Maven's maxUniqueSnapshots cleanup, which runs on a timer, and the
// metadata the work queue writes after a deploy commits.
func (w *world) registerWaits(sc *godog.ScenarioContext) {
	sc.Step(`^within (\d+) seconds GET "([^"]*)" as admin answers (\d+)$`, w.eventuallyStatus)
	sc.Step(
		`^within (\d+) seconds the JSON field "([^"]*)" of GET "([^"]*)" as admin has (\d+) entries$`,
		w.eventuallyLen,
	)
	sc.Step(
		`^within (\d+) seconds the body of GET "([^"]*)" as admin contains "([^"]*)"$`,
		w.eventuallyContains,
	)
	sc.Step(
		`^within (\d+) seconds the body of GET "([^"]*)" as admin does not contain "([^"]*)"$`,
		w.eventuallyLacks,
	)
}

// eventually retries check, each time after a fresh GET of path as admin,
// until it passes or seconds have gone, and returns its last error.
func (w *world) eventually(seconds int, path string, check func() error) error {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		err := w.getAsAdmin(path)
		if err == nil {
			err = check()
		}
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (w *world) eventuallyStatus(seconds int, path string, want int) error {
	return w.eventually(seconds, path, func() error { return w.statusIs(want) })
}

func (w *world) eventuallyLen(seconds int, key, path string, want int) error {
	return w.eventually(seconds, path, func() error { return w.jsonFieldLen(key, want) })
}

func (w *world) eventuallyContains(seconds int, path, want string) error {
	return w.eventually(seconds, path, func() error { return w.bodyContains(want) })
}

func (w *world) eventuallyLacks(seconds int, path, unwanted string) error {
	return w.eventually(seconds, path, func() error { return w.outputLacks(unwanted) })
}
