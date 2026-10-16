package acceptance

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/cucumber/godog"
)

// The registry's real clients (decision 0015): the runner's docker, whose
// daemon must reach the binary on 127.0.0.1, and crane, a pinned tool
// binary named by ARTIFACTWEAVE_CRANE_BIN. Both keep their credentials in
// the scenario's own docker configuration, never the developer's.

func (w *world) registerDocker(sc *godog.ScenarioContext) {
	sc.Step(`^the Docker daemon can reach the registry$`, w.dockerReachable)
	sc.Step(`^I run docker login as "([^"]*)" with password "([^"]*)"$`, w.dockerLogin)
	sc.Step(`^crane is available$`, w.craneAvailable)
	sc.Step(`^I run crane "([^"]*)"$`, w.runCrane)
}

// dockerReachable skips the scenario unless a Docker daemon runs on this
// host: Docker Desktop's runs in a VM, where 127.0.0.1 is not the
// binary's, so only Linux qualifies.
func (w *world) dockerReachable() error {
	if runtime.GOOS != "linux" || exec.Command("docker", "info").Run() != nil {
		return godog.ErrSkip
	}
	return nil
}

// dockerLogin runs docker login against the binary, the password on stdin.
func (w *world) dockerLogin(user, password string) error {
	dir, err := w.dockerConfigDir()
	if err != nil {
		return err
	}
	cmd := exec.Command("docker", "--config", dir, "login", w.expand("{host}"),
		"-u", user, "--password-stdin")
	cmd.Stdin = strings.NewReader(w.expand(password))
	return w.runClient(cmd)
}

func (w *world) craneAvailable() error {
	if os.Getenv("ARTIFACTWEAVE_CRANE_BIN") == "" {
		return godog.ErrSkip
	}
	return nil
}

// runCrane runs crane with its arguments expanded (expand).
func (w *world) runCrane(args string) error {
	dir, err := w.dockerConfigDir()
	if err != nil {
		return err
	}
	cmd := exec.Command(os.Getenv("ARTIFACTWEAVE_CRANE_BIN"), strings.Fields(w.expand(args))...)
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+dir)
	return w.runClient(cmd)
}

// runClient runs a client, keeping its output and exit code for the
// command steps.
func (w *world) runClient(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	w.body, w.lastRC = out, 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		w.lastRC = exitErr.ExitCode()
		return nil
	}
	return err
}

// dockerConfigDir is the scenario's docker configuration directory.
func (w *world) dockerConfigDir() (string, error) {
	if w.dockerConfig == "" {
		dir, err := os.MkdirTemp("", "docker-config-")
		if err != nil {
			return "", err
		}
		w.t.Cleanup(func() { _ = os.RemoveAll(dir) })
		w.dockerConfig = dir
	}
	return w.dockerConfig, nil
}
