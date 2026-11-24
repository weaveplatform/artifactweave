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
