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

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/idempotency"
	platstoretest "github.com/weaveplatform/artifactweave/migration/platform/store/storetest"
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
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestAccReplayCrashHelper$")
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

// ReplayCrashChild is called only by each backend's subprocess test helper.
func ReplayCrashChild(t *testing.T, s platstoretest.Store) {
	t.Helper()
	_, err := s.Replays().
		Do(t.Context(), crashReplayRequest(), func(ctx context.Context) (int64, error) {
			if err := s.Settings().Put(ctx, "api-replay-crash", true); err != nil {
				return 0, fmt.Errorf("crash mutation: %w", err)
			}
			if _, err := fmt.Fprintln(os.Stdout, "replay-mutation-written"); err != nil {
				return 0, fmt.Errorf("crash signal: %w", err)
			}
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(15 * time.Second):
				return 0, context.DeadlineExceeded
			}
		})
	t.Fatalf("crash helper was not killed: %v", err)
}
