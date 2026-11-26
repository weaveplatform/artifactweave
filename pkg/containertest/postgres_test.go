package containertest

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestAccPostgresIsRemovedByItsStop: the server accepts the endpoint's DSN,
// Removed reports the container while it exists, and once the stop function
// has run the engine no longer has it, which is what lets a caller prove it
// left no container behind.
func TestAccPostgresIsRemovedByItsStop(t *testing.T) {
	RequireLinux(t)
	ctx := t.Context()
	ep, stop, err := RunPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			stop()
		}
	})
	conn, err := pgx.Connect(ctx, ep.DSN)
	if err != nil {
		t.Fatal(err)
	}
	var version string
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil ||
		!strings.HasPrefix(version, "17.") {
		t.Fatalf("server_version %q (%v), want PostgreSQL 17", version, err)
	}
	_ = conn.Close(ctx)
	if err := Removed(ctx, ep.ContainerID); err == nil ||
		!strings.Contains(err.Error(), "still present") {
		t.Fatalf("Removed on a running container: %v", err)
	}
	stop()
	stopped = true
	if err := Removed(context.Background(), ep.ContainerID); err != nil {
		t.Fatal(err)
	}
}

// TestAccPostgresStartsFromItsCopy reaches the pinned image through its
// second source when the first source cannot resolve, then uses that image
// to start a server and execute a query.
func TestAccPostgresStartsFromItsCopy(t *testing.T) {
	RequireLinux(t)
	ctx := t.Context()
	image, err := requireImage(ctx, "unavailable.invalid/library/postgres@"+postgresDigest, postgresSources[1])
	if err != nil {
		t.Fatal(err)
	}
	if image != postgresSources[1] {
		t.Fatalf("image %s, want the second source %s", image, postgresSources[1])
	}
	ep, stop, err := runPostgres(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	conn, err := pgx.Connect(ctx, ep.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var value int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&value); err != nil || value != 1 {
		t.Fatalf("query = %d (%v), want 1", value, err)
	}
}
