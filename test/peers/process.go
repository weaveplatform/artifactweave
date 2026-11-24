package peers

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
)

// InstanceTest is the test every instance process runs: the test binary
// that calls Start must declare it, calling RunInstance.
//
//	func TestAccPeerInstance(t *testing.T) { peers.RunInstance(t) }
const InstanceTest = "TestAccPeerInstance"

// instanceConfigEnv hands an instance process its configuration file. Only
// processes Start launches carry it.
const instanceConfigEnv = "ARTIFACTWEAVE_PEER_INSTANCE_CONFIG"

// RunInstance is the body of InstanceTest. In the scenario's own process it
// returns at once. In an instance process it loads the configuration Start
// wrote, through config.Load with no environment or flags, so the host's
// ARTIFACTWEAVE_* variables cannot reach it, and runs app.Run, serve's own
// runtime, router and production costs, until Start closes its standard
// input.
func RunInstance(t *testing.T) {
	path := os.Getenv(instanceConfigEnv)
	if path == "" {
		return
	}
	cfg, err := config.Load(config.LoadOptions{File: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireMasterKey(); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		stop()
	}()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := app.Run(ctx, cfg, log); err != nil {
		t.Fatal(err)
	}
}

// TestCommand returns a command that runs one test of this test binary, the
// way Start runs InstanceTest: with the binary's -test.gocoverdir, so a
// -cover run counts the process's coverage with its own, and env added to
// this process's environment.
func TestCommand(ctx context.Context, test string, env ...string) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"-test.run=^" + test + "$", "-test.count=1", "-test.v"}
	// go test -cover hands the binary a -test.gocoverdir it later reads
	// every counter file from, so the processes' counters join the
	// parent's profile; without it a process would write none.
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+f.Value.String())
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd, nil
}

// startProcess starts InstanceTest over the configuration at path, with its
// output in log, and returns its standard input, whose closing stops it, and
// a function that kills it. done closes once it has exited and *exit holds
// its result.
func startProcess(log *os.File, path string, done chan struct{}, exit *error) (io.WriteCloser, func(), error) {
	ctx, kill := context.WithCancel(context.Background())
	cmd, err := TestCommand(ctx, InstanceTest, instanceConfigEnv+"="+path)
	if err != nil {
		kill()
		_ = log.Close()
		return nil, nil, err
	}
	cmd.Stdout, cmd.Stderr = log, log
	stdin, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		kill()
		_ = log.Close()
		return nil, nil, errors.Join(errors.New("start instance process"), err)
	}
	go func() {
		*exit = cmd.Wait()
		_ = log.Close()
		kill()
		close(done)
	}()
	return stdin, kill, nil
}
