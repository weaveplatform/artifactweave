// Package containertest gates the tests that start containers (decision
// 0015). They need a Docker daemon that runs Linux containers: GitHub's
// Windows runners have a healthy Docker that runs only Windows containers,
// so testcontainers' own health check passes and every Linux image then
// fails to start. Those tests skip there and run on Linux and on macOS with
// Docker Desktop.
package containertest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// AnswerTimeout is how long the Docker engine has to answer before the
// tests that need it skip. A working engine answers in milliseconds, a CI
// runner's included. Docker Desktop on macOS can accept connections on its
// socket and then never answer (#458); testcontainers' own calls take no
// deadline there, so without this bound every Docker-backed test blocked
// until the package's -timeout killed the binary and hid every other
// result in it.
const AnswerTimeout = 5 * time.Second

// errNoAnswer reports an engine that did not answer within the timeout.
var errNoAnswer = errors.New("the Docker engine did not answer")

var (
	checkOnce sync.Once
	checkErr  error
)

// RequireLinux skips t unless a Docker daemon that runs Linux containers
// answers and the run is not -short.
func RequireLinux(t *testing.T) {
	t.Helper()
	skipUnless(t, CheckLinux)
}

// skipUnless skips t in -short runs and whenever check reports an engine
// the test cannot use.
func skipUnless(t *testing.T, check func(context.Context) error) {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode")
	}
	if err := check(context.Background()); err != nil {
		t.Skip(err)
	}
}

// CheckLinux returns an error unless a Docker daemon that runs Linux
// containers answers within AnswerTimeout, for callers without a
// *testing.T. The first call's answer holds for the rest of the test
// binary: an engine that did not answer once is not waited on again by
// every later test, and one that did is not asked again.
func CheckLinux(ctx context.Context) error {
	checkOnce.Do(func() { checkErr = withDeadline(ctx, AnswerTimeout, linuxEngine) })
	return checkErr
}

// withDeadline runs check and returns its error, or errNoAnswer once
// timeout passes without one. check runs on its own goroutine because
// testcontainers creates its client and provider without a context, so
// nothing passed to check can stop those calls; a check that never returns
// leaves that goroutine blocked until the binary exits, which CheckLinux's
// once-per-binary answer bounds to one.
func withDeadline(
	ctx context.Context,
	timeout time.Duration,
	check func(context.Context) error,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- recovered(ctx, check) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("%w within %s: %w", errNoAnswer, timeout, ctx.Err())
	}
}

// recovered runs check and turns a panic into an error: testcontainers
// panics when it finds no Docker host at all, which means the same as an
// engine that does not answer.
func recovered(ctx context.Context, check func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker is not running: %v", r)
		}
	}()
	return check(ctx)
}

// linuxEngine asks the engine testcontainers would use for its health and
// its container OS.
func linuxEngine(ctx context.Context) error {
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return fmt.Errorf("docker is not running: %w", err)
	}
	if err := provider.Health(ctx); err != nil {
		return fmt.Errorf("docker is not healthy: %w", err)
	}
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		return fmt.Errorf("no Docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	res, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("no Docker info: %w", err)
	}
	if res.Info.OSType != "linux" {
		return fmt.Errorf("docker runs %s containers; this needs Linux containers", res.Info.OSType)
	}
	return nil
}
