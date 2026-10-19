package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// The registry's real clients (decision 0015): the runner's docker, whose
// daemon must reach the binary on 127.0.0.1, and crane, oras and cosign,
// pinned tool binaries named by ARTIFACTWEAVE_{CRANE,ORAS,COSIGN}_BIN. All
// keep their credentials in the scenario's own docker configuration, never
// the developer's.

// registryTools maps each pinned tool to the variable naming its binary.
var registryTools = map[string]string{
	"crane":  "ARTIFACTWEAVE_CRANE_BIN",
	"oras":   "ARTIFACTWEAVE_ORAS_BIN",
	"cosign": "ARTIFACTWEAVE_COSIGN_BIN",
}

func (w *world) registerDocker(sc *godog.ScenarioContext) {
	sc.Step(`^the Docker daemon can reach the registry$`, w.dockerReachable)
	sc.Step(`^I run docker login as "([^"]*)" with password "([^"]*)"$`, w.dockerLogin)
	sc.Step(`^I run docker "([^"]*)"$`, w.runDocker)
	sc.Step(`^a buildx builder that pushes to the registry$`, w.buildxBuilder)
	sc.Step(`^(crane|oras|cosign) is available$`, w.toolAvailable)
	sc.Step(`^I run (crane|oras|cosign) "([^"]*)"$`, w.runTool)
	sc.Step(`^I keep the output as "([^"]*)"$`, w.keepOutput)
	sc.Step(`^the auth entry is keyed by the registry URL$`, w.authEntryKeyed)
	sc.Step(`^a layer "([^"]*)" holding "([^"]*)" with content "([^"]*)"$`, w.writeLayer)
	sc.Step(`^a file "([^"]*)" with expanded content:$`, w.writeExpandedFile)
}

// dockerReachable skips the scenario unless a Docker daemon runs on this
// host: Docker Desktop's runs in a VM, where 127.0.0.1 is not the
// binary's, so only Linux qualifies.
func (w *world) dockerReachable() error {
	if runtime.GOOS != "linux" {
		return godog.ErrSkip
	}
	if err := dockerAnswers(); err != nil {
		w.t.Logf("skipping: %v", err)
		return godog.ErrSkip
	}
	return nil
}

var (
	dockerOnce sync.Once
	dockerErr  error
)

// dockerAnswers asks the docker client for the engine's info once per
// test binary, as containertest.CheckLinux asks the API: every @docker and
// client scenario probes, and an engine that did not answer the first
// would only make each of them wait out the deadline again.
func dockerAnswers() error {
	dockerOnce.Do(func() {
		dockerErr = dockerInfo(context.Background(), "docker", containertest.AnswerTimeout)
	})
	return dockerErr
}

// dockerInfo runs docker info and returns why it failed, or that it did
// not answer within timeout. Without the deadline an engine that accepts
// connections and never answers, as Docker Desktop's can (#458), held
// every Docker scenario until the job's timeout (#497).
func dockerInfo(ctx context.Context, docker string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, docker, "info")
	// docker info runs its CLI plugins for their metadata, and a plugin
	// left running would keep the output pipe open after docker itself is
	// killed, so Wait gives up on the pipe a second later.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%s info did not answer within %s", docker, timeout)
	default:
		return fmt.Errorf("%s info: %w: %s", docker, err, firstLine(out))
	}
}

// firstLine is the first line of a command's output, which for docker info
// names the reason, such as the daemon socket it could not reach.
func firstLine(out []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
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

// runDocker runs the docker client against the scenario's configuration,
// in the scenario's file directory, so a build context can be a relative
// path.
func (w *world) runDocker(args string) error {
	dir, err := w.dockerConfigDir()
	if err != nil {
		return err
	}
	cmd := exec.Command(
		"docker",
		append([]string{"--config", dir}, strings.Fields(w.expand(args))...)...)
	cmd.Dir = w.dir
	return w.runClient(cmd)
}

// buildxBuilder creates a docker-container buildx builder in the
// scenario's docker configuration and selects it, so buildx build pushes
// multi-platform images, which the daemon's own builder does only with the
// containerd image store. Plain docker build keeps the default builder
// whatever is selected, so the builder's name is kept as {builder} for
// BUILDX_BUILDER. Its BuildKit shares the host's network to reach the
// binary on 127.0.0.1, speaking plain HTTP to it, and the scenario's end
// removes it.
func (w *world) buildxBuilder() error {
	dir, err := w.dockerConfigDir()
	if err != nil {
		return err
	}
	conf := filepath.Join(dir, "buildkitd.toml")
	if err := os.WriteFile(conf,
		fmt.Appendf(nil, "[registry.%q]\n  http = true\n", w.expand("{host}")), 0o600); err != nil {
		return err
	}
	name := "artifactweave-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	out, err := exec.Command("docker", "--config", dir, "buildx", "create", "--name", name,
		"--driver", "docker-container", "--driver-opt", "network=host",
		"--buildkitd-config", conf, "--use", "--bootstrap").CombinedOutput()
	if err != nil {
		return fmt.Errorf("buildx create: %w\n%s", err, out)
	}
	w.closers = append(w.closers, func() {
		_ = exec.Command("docker", "--config", dir, "buildx", "rm", "--force", name).Run()
	})
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["builder"] = name
	return nil
}

// toolAvailable skips the scenario unless the tool's pinned binary is
// named, so the features pass locally without the downloads CI performs.
func (w *world) toolAvailable(tool string) error {
	if os.Getenv(registryTools[tool]) == "" {
		return godog.ErrSkip
	}
	return nil
}

// runTool runs a pinned tool with its arguments expanded (expand), in the
// scenario's file directory. DOCKER_CONFIG gives crane, oras and cosign the
// scenario's credentials; cosign's key pair has an empty password, and
// COSIGN_YES answers its confirmation prompts.
func (w *world) runTool(tool, args string) error {
	dir, err := w.dockerConfigDir()
	if err != nil {
		return err
	}
	cmd := exec.Command(os.Getenv(registryTools[tool]), strings.Fields(w.expand(args))...)
	cmd.Dir = w.dir
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+dir, "COSIGN_PASSWORD=", "COSIGN_YES=true")
	return w.runClient(cmd)
}

// authEntryKeyed checks the body of a Docker auth endpoint: one config.json
// entry, keyed by the registry's URL, with the auth string set. A JSON
// field step cannot name the key, whose dots and colon it would split.
func (w *world) authEntryKeyed() error {
	var doc map[string]struct {
		Auth string `json:"auth"`
	}
	if err := json.Unmarshal(w.body, &doc); err != nil {
		return fmt.Errorf("body is not a config.json entry: %w: %s", err, w.body)
	}
	if e, ok := doc[w.url]; len(doc) != 1 || !ok || e.Auth == "" {
		return fmt.Errorf("want one entry keyed %q with auth set, got %s", w.url, w.body)
	}
	return nil
}

// keepOutput keeps the last command's output, trimmed, for {name} in later
// steps: a digest crane printed, for example.
func (w *world) keepOutput(name string) error {
	v := strings.TrimSpace(string(w.body))
	if v == "" || strings.ContainsAny(v, "\n ") {
		return fmt.Errorf("want one word of output to keep, got:\n%s", w.body)
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars[name] = v
	return nil
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

// writeLayer writes a gzipped tar holding one file, a layer crane can
// append to build an image without pulling a base from the network.
func (w *world) writeLayer(name, entry, content string) error {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: entry, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	if _, err := io.WriteString(tw, content); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return w.writeFile(name, &godog.DocString{Content: buf.String()})
}

// writeExpandedFile writes a scenario file with {host}, {digest} and the
// other placeholders expanded, such as the image file
// jf rt build-docker-create reads.
func (w *world) writeExpandedFile(name string, content *godog.DocString) error {
	return w.writeFile(name, &godog.DocString{Content: w.expand(content.Content)})
}

// TestDockerInfo checks the probe against stand-ins for the docker client:
// an engine that never answers costs the deadline and reads as unreachable,
// even when a plugin it started holds the output open past it (#497); one
// that fails says why; one that answers is reachable.
func TestDockerInfo(t *testing.T) {
	dir := t.TempDir()
	fake := func(name, script string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// The stand-ins that hang get a short deadline and outlast the bound
	// checked below, so only the deadline, and WaitDelay for the pipe, can
	// end them in time. The others get the real one: starting a fresh
	// script can itself take a few hundred milliseconds on macOS.
	const short = 300 * time.Millisecond
	for name, c := range map[string]struct {
		script  string
		timeout time.Duration
		want    string
	}{
		"never-answers": {"exec sleep 10", short, "did not answer within 300ms"},
		// The background sleep stands for a plugin that outlives docker:
		// it holds the output open after docker is killed.
		"plugin-holds-output": {"sleep 10 &\nexec sleep 10", short, "did not answer within 300ms"},
		"daemon-down": {
			"echo 'Cannot connect to the Docker daemon at unix:///x.sock.' >&2\nexit 1",
			containertest.AnswerTimeout,
			"exit status 1: Cannot connect to the Docker daemon at unix:///x.sock.",
		},
		"answers": {"echo 'Server Version: 28.0.0'", containertest.AnswerTimeout, ""},
	} {
		start := time.Now()
		err := dockerInfo(context.Background(), fake(name, c.script), c.timeout)
		took := time.Since(start)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", name, err, c.want)
		case took > 5*time.Second:
			t.Errorf("%s: took %s, past the deadline and the pipe's grace", name, took)
		}
	}
}
