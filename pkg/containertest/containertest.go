// Package containertest gates the tests that start containers (decision
// 0015). They need a Docker daemon that runs Linux containers: GitHub's
// Windows runners have a healthy Docker that runs only Windows containers,
// so testcontainers' own health check passes and every Linux image then
// fails to start. Those tests skip there and run on Linux and on macOS with
// Docker Desktop.
package containertest

import (
	"context"
	"fmt"
	"testing"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// RequireLinux skips t unless a Docker daemon that runs Linux containers is
// available and the run is not -short.
func RequireLinux(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	if err := CheckLinux(context.Background()); err != nil {
		t.Skip(err)
	}
}

// CheckLinux returns an error unless a Docker daemon that runs Linux
// containers is available, for callers without a *testing.T.
func CheckLinux(ctx context.Context) error {
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
