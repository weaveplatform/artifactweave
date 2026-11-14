package storetest

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
	platstoretest "github.com/weaveplatform/artifactweave/internal/platform/store/storetest"
)

func crashReplayRequest() idempotency.ReplayRequest {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("process-crash")))
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	return idempotency.ReplayRequest{
		Scope:       digest,
		Key:         digest,
		Fingerprint: digest,
		Now:         now,
		Expires:     now.Add(time.Hour),
	}
}

// ReplayCrash kills a separate process after its transactional mutation but
// before its receipt commits. Both the effect and receipt must roll back. It
// uses only s's settings and replay stores, so it takes the platform's Store.
func ReplayCrash(t *testing.T, s platstoretest.Store, env []string) {
	t.Helper()
	killInTransaction(t, "TestAccReplayCrashHelper", env)
	var value bool
	if err := s.Settings().
		Get(t.Context(), "api-replay-crash", &value); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("crashed mutation survived: %v, %v", value, err)
	}
	out, err := s.Replays().
		Do(t.Context(), crashReplayRequest(), func(ctx context.Context) (int64, error) {
			return 1, s.Settings().Put(ctx, "api-replay-crash", true)
		})
	if err != nil || out.Replayed {
		t.Fatalf("retry after process crash: %+v, %v", out, err)
	}
}

// KeyedReplayCrash is ReplayCrash through the replay that keyed operations
// share (idempotency.Replay), as the router runs a keyed request: the child
// is killed inside the effect, after its mutation and before the claim, the
// effect and the receipt commit, and a retry with the same key must run the
// effect anew and pass its reload. The child is each backend's
// TestAccKeyedReplayCrashHelper, which calls KeyedReplayCrashChild.
func KeyedReplayCrash(t *testing.T, s platstoretest.Store, env []string) {
	t.Helper()
	killInTransaction(t, "TestAccKeyedReplayCrashHelper", env)
	var value bool
	if err := s.Settings().
		Get(t.Context(), "keyed-replay-crash", &value); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("crashed keyed mutation survived: %v, %v", value, err)
	}
	reloaded := false
	out, err := keyedCrashReplay(s).Do(t.Context(), crashReplayRequest(), idempotency.Effect{
		Run: func(ctx context.Context) (int64, error) {
			return 1, s.Settings().Put(ctx, "keyed-replay-crash", true)
		},
		Reload: func(context.Context, idempotency.ReplayReceipt) error {
			reloaded = true
			return nil
		},
	})
	if err != nil || out.Replayed || !reloaded {
		t.Fatalf("keyed retry after process crash: %+v, %v, reloaded %v", out, err, reloaded)
	}
}

// KeyedReplayCrashChild is called only by each backend's subprocess test
// helper for KeyedReplayCrash.
func KeyedReplayCrashChild(t *testing.T, s platstoretest.Store) {
	t.Helper()
	_, err := keyedCrashReplay(s).Do(t.Context(), crashReplayRequest(), idempotency.Effect{
		Run: func(ctx context.Context) (int64, error) {
			if err := s.Settings().Put(ctx, "keyed-replay-crash", true); err != nil {
				return 0, fmt.Errorf("keyed crash mutation: %w", err)
			}
			return 0, awaitKill(ctx)
		},
		Reload: func(context.Context, idempotency.ReplayReceipt) error { return nil },
	})
	t.Fatalf("keyed crash helper was not killed: %v", err)
}

func keyedCrashReplay(s platstoretest.Store) idempotency.Replay {
	return idempotency.Replay{Store: s.Replays(), Tx: s}
}

// killInTransaction runs the test binary's helper test in a child process
// with env, waits for it to report that its mutation is written, and kills
// it there, inside its transaction.
func killInTransaction(t *testing.T, helper string, env []string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^"+helper+"$")
	cmd.Env = append(os.Environ(), env...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := false
	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		if lines.Text() == "replay-mutation-written" {
			ready = true
			break
		}
	}
	if ready {
		if err := cmd.Process.Kill(); err != nil {
			t.Error(err)
		}
	}
	waitErr := cmd.Wait()
	if !ready || waitErr == nil {
		t.Fatalf("child was not killed inside the transaction: ready=%v wait=%v", ready, waitErr)
	}
}

// awaitKill tells the parent that the child's mutation is written, then
// waits to be killed; it returns only if the kill never comes.
func awaitKill(ctx context.Context) error {
	if _, err := fmt.Fprintln(os.Stdout, "replay-mutation-written"); err != nil {
		return fmt.Errorf("crash signal: %w", err)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("awaiting the kill: %w", ctx.Err())
	case <-time.After(15 * time.Second):
		return context.DeadlineExceeded
	}
}

// ReplayCrashChild is called only by each backend's subprocess test helper.
func ReplayCrashChild(t *testing.T, s platstoretest.Store) {
	t.Helper()
	_, err := s.Replays().
		Do(t.Context(), crashReplayRequest(), func(ctx context.Context) (int64, error) {
			if err := s.Settings().Put(ctx, "api-replay-crash", true); err != nil {
				return 0, fmt.Errorf("crash mutation: %w", err)
			}
			return 0, awaitKill(ctx)
		})
	t.Fatalf("crash helper was not killed: %v", err)
}
