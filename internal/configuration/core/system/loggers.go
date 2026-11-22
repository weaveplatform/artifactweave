package system

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/logging"
)

// Timer is a scheduled expiration. Stop reports whether its callback was stopped.
type Timer interface{ Stop() bool }

// LoggerDeps supplies the logging mechanism and guarded current-administrator
// check. AfterFunc defaults to time.AfterFunc and permits deterministic clocks.
type LoggerDeps struct {
	Control   *logging.Control
	Authorize func(context.Context) error
	AfterFunc func(time.Duration, func()) Timer
}

// Loggers owns one temporary override and its timer. Close restores the
// original handler's current decision and joins any callback already running.
type Loggers struct {
	mu      sync.Mutex
	deps    LoggerDeps
	active  *loggerSession
	closed  bool
	workers sync.WaitGroup
}

type loggerSession struct {
	timer Timer
	done  chan struct{}
}

// NewLoggers constructs an idle service without starting a timer or worker.
func NewLoggers(d LoggerDeps) *Loggers {
	if d.AfterFunc == nil {
		d.AfterFunc = func(delay time.Duration, fn func()) Timer { return time.AfterFunc(delay, fn) }
	}
	return &Loggers{deps: d}
}

// Set validates the complete request before changing any runtime decision.
func (s *Loggers) Set(ctx context.Context, classes map[string]string, seconds int) error {
	levels, err := loggerLevels(classes, seconds)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(); err != nil {
		return err
	}
	if err := s.deps.Control.Change(levels, func() error {
		if err := s.deps.Authorize(ctx); err != nil {
			return err
		}
		if s.active != nil {
			return fmt.Errorf("%w: delete the active logger override first", configapi.ErrInvalidLoggerSettings)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("set temporary logger levels: %w", err)
	}
	s.workers.Add(1)
	session := &loggerSession{done: make(chan struct{})}
	s.active = session
	session.timer = s.deps.AfterFunc(time.Duration(seconds)*time.Second, func() {
		defer s.workers.Done()
		defer close(session.done)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active == session {
			s.active = nil
			s.deps.Control.Clear()
		}
	})
	return nil
}

// Delete authorizes against current authority even when no session is active.
func (s *Loggers) Delete(ctx context.Context) error {
	s.mu.Lock()
	if err := s.available(); err != nil {
		s.mu.Unlock()
		return err
	}
	err := s.deps.Control.Change(nil, func() error { return s.deps.Authorize(ctx) })
	var done <-chan struct{}
	if err == nil {
		done = s.stopLocked()
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	if err != nil {
		return fmt.Errorf("delete temporary logger levels: %w", err)
	}
	return nil
}

// Level reads only after the guarded current-administrator check succeeds.
func (s *Loggers) Level(ctx context.Context, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.available(); err != nil {
		return "", err
	}
	if err := s.deps.Authorize(ctx); err != nil {
		return "", fmt.Errorf("read logger level: %w", err)
	}
	return loggerLevelName(s.deps.Control.Level(ctx, name)), nil
}

func (s *Loggers) available() error {
	if s.closed || s.deps.Control == nil || s.deps.Authorize == nil {
		return fmt.Errorf("logger controls unavailable: %w", kernel.ErrStore)
	}
	return nil
}

func (s *Loggers) stopLocked() <-chan struct{} {
	session := s.active
	s.active = nil
	if session == nil {
		return nil
	}
	if session.timer.Stop() {
		close(session.done)
		s.workers.Done()
	}
	return session.done
}

// Close is safe to repeat and prevents later administration from reviving work.
func (s *Loggers) Close() {
	s.mu.Lock()
	s.closed = true
	done := s.stopLocked()
	if s.deps.Control != nil {
		s.deps.Control.Clear()
	}
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	s.workers.Wait()
}

func loggerLevels(classes map[string]string, seconds int) (map[string]slog.Level, error) {
	if len(classes) < 1 || len(classes) > 100 {
		return nil, fmt.Errorf("%w: classes must contain 1 to 100 loggers", configapi.ErrInvalidLoggerSettings)
	}
	if seconds < 1 || uint64(seconds) > uint64(math.MaxInt64/int64(time.Second)) {
		return nil, fmt.Errorf("%w: timer must be positive and representable in seconds", configapi.ErrInvalidLoggerSettings)
	}
	levels := make(map[string]slog.Level, len(classes))
	for name, level := range classes {
		switch level {
		case "trace", "debug":
			levels[name] = slog.LevelDebug
		case "info":
			levels[name] = slog.LevelInfo
		case "warn":
			levels[name] = slog.LevelWarn
		case "error", "fatal":
			levels[name] = slog.LevelError
		default:
			return nil, fmt.Errorf("%w: invalid logger level", configapi.ErrInvalidLoggerSettings)
		}
	}
	return levels, nil
}

func loggerLevelName(level slog.Level) string {
	switch level {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}
