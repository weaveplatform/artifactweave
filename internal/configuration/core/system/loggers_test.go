package system_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/logging"
)

func loggerService(authorize func(context.Context) error) (*system.Loggers, *slog.Logger) {
	control := logging.New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	logger := control.Logger("artifactweave.remote")
	return system.NewLoggers(system.LoggerDeps{Control: control, Authorize: authorize}), logger
}
func loggerAllowed(context.Context) error { return nil }

func TestUnitLoggerExpiryDeleteReplacementAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, logger := loggerService(loggerAllowed)
		defer service.Close()
		ctx := t.Context()
		set := func(level string, seconds int) {
			t.Helper()
			if err := service.Set(ctx, map[string]string{"artifactweave.remote": level}, seconds); err != nil {
				t.Fatal(err)
			}
		}
		set("debug", 1)
		if !logger.Enabled(ctx, slog.LevelDebug) {
			t.Fatal("Set did not change actual logging")
		}
		if got, err := service.Level(ctx, "artifactweave.remote"); err != nil || got != "debug" {
			t.Fatal(got, err)
		}
		if err := service.Set(ctx, map[string]string{"artifactweave.remote": "error"}, 30); !errors.Is(err, configapi.ErrInvalidLoggerSettings) {
			t.Fatal("active session overwritten", err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if logger.Enabled(ctx, slog.LevelDebug) {
			t.Fatal("expiration did not restore original level")
		}
		set("trace", 1)
		if err := service.Delete(ctx); err != nil {
			t.Fatal(err)
		}
		set("fatal", 10)
		time.Sleep(time.Second)
		synctest.Wait()
		if got, err := service.Level(ctx, "artifactweave.remote"); err != nil || got != "error" {
			t.Fatal("old timer replaced newer session", got, err)
		}
		if logger.Enabled(ctx, slog.LevelWarn) {
			t.Fatal("replacement threshold not applied")
		}
		service.Close()
		service.Close()
		if !logger.Enabled(ctx, slog.LevelInfo) {
			t.Fatal("close did not restore baseline")
		}
		if err := service.Set(ctx, map[string]string{"artifactweave.remote": "debug"}, 1); !errors.Is(err, kernel.ErrStore) {
			t.Fatal("closed service revived", err)
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
	})
}

func TestUnitLoggerValidationChangesNothing(t *testing.T) {
	service, logger := loggerService(loggerAllowed)
	defer service.Close()
	cases := []struct {
		name    string
		classes map[string]string
		seconds int
	}{
		{"missing classes", nil, 1},
		{"empty classes", map[string]string{}, 1},
		{"invalid level", map[string]string{"artifactweave.remote": "DEBUG"}, 1},
		{"zero timer", map[string]string{"artifactweave.remote": "debug"}, 0},
		{"negative timer", map[string]string{"artifactweave.remote": "debug"}, -1},
		{"overflow timer", map[string]string{"artifactweave.remote": "debug"}, math.MaxInt},
	}
	tooMany := make(map[string]string, 101)
	for i := range 101 {
		tooMany[fmt.Sprint(i)] = "debug"
	}
	cases = append(cases, struct {
		name    string
		classes map[string]string
		seconds int
	}{"too many classes", tooMany, 1})
	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			if err := service.Set(t.Context(), entry.classes, entry.seconds); !errors.Is(err, configapi.ErrInvalidLoggerSettings) {
				t.Fatal(err)
			}
			if logger.Enabled(t.Context(), slog.LevelDebug) {
				t.Fatal("invalid request changed logging")
			}
		})
	}
	delete(tooMany, "100")
	if err := service.Set(t.Context(), tooMany, 60); err != nil {
		t.Fatal("100 entries refused", err)
	}
	if logger.Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("unknown loggers changed registered logger")
	}
	if got, err := service.Level(t.Context(), "unknown"); err != nil || got != "info" {
		t.Fatal(got, err)
	}
	if err := service.Delete(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"info", "warn", "error"} {
		if err := service.Set(t.Context(), map[string]string{"artifactweave.remote": level}, 60); err != nil {
			t.Fatal(err)
		}
		if got, err := service.Level(t.Context(), "artifactweave.remote"); err != nil || got != level {
			t.Fatal(got, err)
		}
		if err := service.Delete(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Delete(t.Context()); err != nil {
		t.Fatal("empty delete", err)
	}
}

func TestUnitLoggerAuthorityFailureChangesNothing(t *testing.T) {
	marker := kernel.ErrForbidden
	refused := false
	service, logger := loggerService(func(context.Context) error {
		if refused {
			return marker
		}
		return nil
	})
	defer service.Close()
	ctx := t.Context()
	refused = true
	if err := service.Set(ctx, map[string]string{"artifactweave.remote": "debug"}, 60); !errors.Is(err, marker) {
		t.Fatal(err)
	}
	if logger.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("unauthorized Set changed logging")
	}
	refused = false
	if err := service.Set(ctx, map[string]string{"artifactweave.remote": "debug"}, 60); err != nil {
		t.Fatal(err)
	}
	refused = true
	if err := service.Delete(ctx); !errors.Is(err, marker) {
		t.Fatal(err)
	}
	if !logger.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("unauthorized Delete changed logging")
	}
	if _, err := service.Level(ctx, "artifactweave.remote"); !errors.Is(err, marker) {
		t.Fatal("unauthorized read succeeded", err)
	}
	service.Close()
	if err := service.Delete(ctx); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	if _, err := service.Level(ctx, "artifactweave.remote"); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
	unavailable := system.NewLoggers(system.LoggerDeps{})
	defer unavailable.Close()
	if err := unavailable.Set(ctx, map[string]string{"x": "debug"}, 1); !errors.Is(err, kernel.ErrStore) {
		t.Fatal(err)
	}
}

func TestUnitLoggerConcurrentUpdatesAndDelete(t *testing.T) {
	service, logger := loggerService(loggerAllowed)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 100 {
				err := service.Set(t.Context(), map[string]string{"artifactweave.remote": "debug"}, 60)
				if err != nil && !errors.Is(err, configapi.ErrInvalidLoggerSettings) {
					t.Error(err)
				}
				if err := service.Delete(t.Context()); err != nil {
					t.Error(err)
				}
				_ = logger.Enabled(t.Context(), slog.LevelDebug)
			}
		})
	}
	group.Wait()
	service.Close()
	if logger.Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("concurrent controls left override after close")
	}
}

type pendingLoggerTimer struct {
	stopped chan struct{}
	pending bool
}

func (timer pendingLoggerTimer) Stop() bool {
	if timer.stopped != nil {
		close(timer.stopped)
	}
	return !timer.pending
}

func TestUnitLoggerPendingOldCallbackCannotClearReplacement(t *testing.T) {
	control := logging.New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	logger := control.Logger("artifactweave.remote")
	var callback func()
	scheduled := 0
	stopped := make(chan struct{})
	service := system.NewLoggers(system.LoggerDeps{Control: control, Authorize: loggerAllowed, AfterFunc: func(_ time.Duration, fn func()) system.Timer {
		scheduled++
		if scheduled == 1 {
			callback = fn
			return pendingLoggerTimer{stopped: stopped, pending: true}
		}
		return pendingLoggerTimer{}
	}})
	defer service.Close()
	if err := service.Set(t.Context(), map[string]string{"artifactweave.remote": "debug"}, 1); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- service.Delete(t.Context()) }()
	<-stopped
	if err := service.Set(t.Context(), map[string]string{"artifactweave.remote": "error"}, 60); err != nil {
		t.Fatal(err)
	}
	callback()
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if logger.Enabled(t.Context(), slog.LevelWarn) {
		t.Fatal("pending old callback restored over replacement")
	}
	service.Close()
	if !logger.Enabled(t.Context(), slog.LevelInfo) {
		t.Fatal("Close did not clear replacement")
	}
}
