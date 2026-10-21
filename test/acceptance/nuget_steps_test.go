package acceptance

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cucumber/godog"
)

func (w *world) registerNuGet(sc *godog.ScenarioContext) {
	sc.Step(`^I run jf dotnet client "([^"]*)"$`, w.runJFDotnet)
	sc.Step(
		`^the NuGet lockfile "([^"]*)" pins "([^"]*)" to the package "([^"]*)"$`,
		w.nugetLockfilePins,
	)
}

// JFrog runs the same SDK as the native scenario; mounting the pinned Linux
// binary avoids another image and keeps configuration/build records isolated.
func (w *world) runJFDotnet(args string) error {
	if err := w.jfAvailable(); err != nil {
		return err
	}
	bin, err := filepath.Abs(os.Getenv("ARTIFACTWEAVE_JF_BIN"))
	if err != nil {
		return err
	}
	argv := w.clientArgs(args)
	flags := []string{
		"--entrypoint", "/usr/local/bin/jf",
		"-v", bin + ":/usr/local/bin/jf:ro",
		"-e", "JFROG_CLI_HOME_DIR=/work/.jfrog-cli",
		"-e", "JFROG_CLI_TEMP_DIR=/work/.jfrog-tmp",
		"-e", "JFROG_CLI_REPORT_USAGE=false",
		"-e", "JFROG_CLI_AVOID_NEW_VERSION_WARNING=true",
		"-e", "JFROG_RUN_NATIVE=true",
		"-e", "DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"-e", "DOTNET_NOLOGO=1",
		"-e", "DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1",
		"-e", "DOTNET_GENERATE_ASPNET_CERTIFICATE=false",
		"-e", "NUGET_XMLDOC_MODE=skip",
		"-e", "CI=true",
	}
	// JF delegates push to the same SDK command as runDotnet; retain its
	// documented fallback for the pinned .NET 11 RC2 teardown hang.
	if len(argv) >= 3 && argv[0] == "dotnet" && argv[1] == "nuget" && argv[2] == "push" {
		flags = append(flags, "-e", "DOTNET_CLI_ENABLEAOT=false")
	}
	return w.runContainer(w.dotnetImage, flags, argv)
}

// Read the lockfile itself, not NuGet's sidecar digest: this proves that the
// consumer pinned the exact bytes produced by pack and returned by the server.
func (w *world) nugetLockfilePins(lock, id, pkg string) error {
	raw, err := os.ReadFile(filepath.Join(w.dir, lock))
	if err != nil {
		return err
	}
	var document struct {
		Dependencies map[string]map[string]struct {
			ContentHash string `json:"contentHash"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(w.dir, pkg))
	if err != nil {
		return err
	}
	digest := sha512.Sum512(body)
	want := base64.StdEncoding.EncodeToString(digest[:])
	found := false
	for framework, packages := range document.Dependencies {
		if dependency, ok := packages[id]; ok {
			found = true
			if dependency.ContentHash != want {
				return fmt.Errorf(
					"%s/%s contentHash %q, want %q",
					framework,
					id,
					dependency.ContentHash,
					want,
				)
			}
		}
	}
	if !found {
		return fmt.Errorf("%s has no dependency %s", lock, id)
	}
	return nil
}
