package containertest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// postgresDigest pins postgres:17-alpine, the multi-platform index digest,
// the release internal/platform/sql/sqltest names by tag; bump it
// deliberately, here only, and check the second source serves it.
const postgresDigest = "sha256:f9cf3e1f0066f0eb0c48707d864f0ac5941e9ff454a072077d97a4d278d07d73"

// PostgresImage is PostgreSQL 17 on Alpine.
const PostgresImage = "postgres:17-alpine@" + postgresDigest

// postgresSources: mirror.gcr.io, Google's cache of Docker Hub, served the
// digest when it was pinned (checked 2026-11-24).
var postgresSources = []string{PostgresImage, "mirror.gcr.io/library/postgres@" + postgresDigest}

// PostgresEndpoint addresses a PostgreSQL server of its own.
type PostgresEndpoint struct {
	// DSN connects to the server's maintenance database as its superuser,
	// with sslmode=disable; a caller creates the databases it needs there.
	DSN string
	// ContainerID names the container, for Removed.
	ContainerID string
}

// Postgres starts a PostgreSQL server for t alone and removes it when t
// ends, or skips t where Docker cannot run Linux containers.
// internal/platform/sql/sqltest's PostgresDSN shares one server across a test
// binary instead; this one is for a caller that must prove it leaves no
// container behind.
func Postgres(t *testing.T) PostgresEndpoint {
	t.Helper()
	RequireLinux(t)
	ep, stop, err := RunPostgres(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ep
}

// RunPostgres starts a PostgreSQL server and returns its endpoint and a
// function that removes it, for callers without a *testing.T; check
// CheckLinux first. It returns once the server accepts connections.
func RunPostgres(ctx context.Context) (PostgresEndpoint, func(), error) {
	image, err := requireImage(ctx, postgresSources...)
	if err != nil {
		return PostgresEndpoint{}, nil, fmt.Errorf("start postgres: %w", err)
	}
	ctr, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("aw"),
		tcpostgres.WithPassword("aw"),
		tcpostgres.BasicWaitStrategies(),
	)
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return PostgresEndpoint{}, nil, fmt.Errorf("start postgres: %w", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return PostgresEndpoint{}, nil, fmt.Errorf("postgres endpoint: %w", err)
	}
	return PostgresEndpoint{DSN: dsn, ContainerID: ctr.GetContainerID()}, stop, nil
}

// Removed returns nil once the engine no longer has the container id names,
// and an error naming it while the engine still has it, running or not.
func Removed(ctx context.Context, id string) error {
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	_, err = cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("inspect container %s: %w", id, err)
	default:
		return errors.New("container " + id + " is still present")
	}
}
