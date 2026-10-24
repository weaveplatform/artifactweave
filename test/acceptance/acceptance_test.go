// Package acceptance runs the Gherkin features in features/ against the real
// artifactweave binary (decision record 0015). The binary is built once with
// -cover; when ARTIFACTWEAVE_GOCOVERDIR is set (make accept) every process
// writes coverage there so acceptance runs count toward the merged gate.
//
// The suite runs on Linux in CI. Windows runners cannot run the container
// backed scenarios and macOS runners have no Docker, so both skip unless a
// developer opts in locally.
package acceptance

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/cucumber/godog"
)

const module = "github.com/weaveplatform/artifactweave"

// binary is the built artifactweave path.
var binary string

// coverDir receives coverage from the binary when set.
var coverDir = os.Getenv("ARTIFACTWEAVE_GOCOVERDIR")

func TestMain(m *testing.M) {
	flag.Parse()
	if skip, why := skipReason(); skip {
		fmt.Fprintln(os.Stderr, "skipping acceptance: "+why)
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "artifactweave-accept-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := func() int {
		defer func() { _ = os.RemoveAll(dir) }()
		out := filepath.Join(dir, "artifactweave")
		args := []string{"build", "-o", out}
		if coverDir != "" {
			coverpkg := os.Getenv("ARTIFACTWEAVE_ACCEPT_COVERPKG")
			if coverpkg == "" {
				coverpkg = module + "/..."
			} else {
				// The executable's main package must be instrumented for Go to
				// emit covmeta/covcounters for a selected package set.
				coverpkg += "," + module + "/cmd/artifactweave"
			}
			args = append(args, "-cover", "-covermode=atomic", "-coverpkg="+coverpkg)
		}
		cmd := exec.Command("go", append(args, "../../cmd/artifactweave")...)
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if b, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(
				os.Stderr,
				"cannot build cmd/artifactweave for acceptance (does it exist yet?): %v\n%s",
				err,
				b,
			)
			return 1
		}
		binary = out
		return m.Run()
	}()
	os.Exit(code)
}

// skipReason decides whether this OS runs the suite. The CI matrix runs unit
// tests on every OS, so the skip must be a clean exit rather than a failure.
func skipReason() (bool, string) {
	switch runtime.GOOS {
	case "windows":
		return true, "acceptance runs on Linux only (decision 0015)"
	case "darwin":
		if os.Getenv("ARTIFACTWEAVE_ACCEPT_LOCAL") == "" {
			return true, "set ARTIFACTWEAVE_ACCEPT_LOCAL=1 to run on macOS"
		}
	}
	return false, ""
}

func TestAccFeatures(t *testing.T) {
	var scenarios atomic.Int64
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				scenarios.Add(1)
				return ctx, nil
			})
			newWorld(t).register(sc)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
			Strict:   true,
			Tags:     os.Getenv("ARTIFACTWEAVE_ACCEPT_TAGS"),
		},
	}
	result := suite.Run()
	if scenarios.Load() == 0 {
		t.Fatalf(
			"acceptance tag filter %q selected zero scenarios",
			os.Getenv("ARTIFACTWEAVE_ACCEPT_TAGS"),
		)
	}
	if result != 0 {
		t.Fatal("acceptance features failed")
	}
}
