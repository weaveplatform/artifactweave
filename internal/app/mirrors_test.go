package app

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestOptimizeRunsRepairNow checks that the optimize trigger reaches the
// repair loop: with the interval an hour away, only the trigger can start
// the logged pass.
func TestOptimizeRunsRepairNow(t *testing.T) {
	orig := repairEvery
	repairEvery = time.Hour
	t.Cleanup(func() { repairEvery = orig })
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := testConfig(t)
	cfg.Blob = mirrorConfig()
	ctx := context.Background()
	rt, err := openRuntime(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	admin := rt.mirrorsAdmin()
	if admin == nil {
		t.Fatal("a mirror tier has no admin")
	}
	st, err := admin.Status(ctx)
	if err != nil || len(st) != 1 || st[0].Tier != "blob" || len(st[0].Members) != 3 ||
		st[0].Members[0].Kind != "fs" {
		t.Fatalf("status %+v, %v", st, err)
	}
	// A second optimize while the first pass is queued must not block.
	admin.Optimize()
	admin.Optimize()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(),
		"blob mirror repair: nothing to do"); {
		if time.Now().After(deadline) {
			t.Fatalf("optimize started no repair pass:\n%s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := rt.close(ctx); err != nil {
		t.Fatal(err)
	}
	// The store is closed, so the status queries fail.
	if _, err := admin.Status(ctx); err == nil {
		t.Fatal("status after close succeeded")
	}
}

func TestNoMirrorNoAdmin(t *testing.T) {
	ctx := context.Background()
	rt, err := openRuntime(ctx, testConfig(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.close(ctx) })
	if rt.mirrorsAdmin() != nil {
		t.Fatal("an fs-only runtime reports mirrors")
	}
}
