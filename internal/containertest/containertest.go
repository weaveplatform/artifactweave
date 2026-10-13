// Package containertest gates the tests that start containers (decision
// 0015). They need a Docker daemon that runs Linux containers: GitHub's
// Windows runners have a healthy Docker that runs only Windows containers,
// so testcontainers' own health check passes and every Linux image then
// fails to start. Those tests skip there and run on Linux and on macOS with
// Docker Desktop.
package containertest

import (
	"context"
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
	ctx := context.Background()
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		t.Skipf("no Docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()
	res, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		t.Skipf("no Docker info: %v", err)
	}
	if res.Info.OSType != "linux" {
		t.Skipf("Docker runs %s containers; this test needs Linux containers", res.Info.OSType)
	}
}
