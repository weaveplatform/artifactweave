package logging

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
)

// Control changes the level decision of registered loggers while preserving
// their sink, attributes, groups and redaction. Names are registered by Logger,
// never by an administration request.
type Control struct {
	mu     sync.RWMutex
	base   *slog.Logger
	names  map[string]struct{}
	levels map[string]slog.Level
}

// New builds a process-local control. A nil logger uses slog.Default().
func New(base *slog.Logger) *Control {
	if base == nil {
		base = slog.Default()
	}
	return &Control{base: base, names: map[string]struct{}{"artifactweave": {}}}
}

// Logger registers name and binds it to the original structured sink.
func (c *Control) Logger(name string) *slog.Logger {
	c.mu.Lock()
	c.names[name] = struct{}{}
	c.mu.Unlock()
	return slog.New(handler{control: c, name: name, sink: c.base.Handler()}).With("logger", name)
}

// Change publishes a complete replacement only after settle succeeds. The
// decision lock prevents records from observing an uncommitted change.
func (c *Control) Change(levels map[string]slog.Level, settle func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := settle(); err != nil {
		return fmt.Errorf("settle log levels: %w", err)
	}
	c.levels = maps.Clone(levels)
	return nil
}

// Clear discards overrides; the original handler decides levels again.
func (c *Control) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.levels = nil
}

// Level returns a known logger's effective level, or the root's current level
// for an unknown name. The original handler remains the baseline authority.
func (c *Control) Level(ctx context.Context, name string) slog.Level {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, known := c.names[name]; !known {
		name = "artifactweave"
	}
	if level, ok := c.override(name); ok {
		return level
	}
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn} {
		if c.base.Enabled(ctx, level) {
			return level
		}
	}
	return slog.LevelError
}

func (c *Control) override(name string) (slog.Level, bool) {
	for name != "" {
		if _, known := c.names[name]; known {
			if level, ok := c.levels[name]; ok {
				return level, true
			}
		}
		dot := strings.LastIndexByte(name, '.')
		if dot < 0 {
			break
		}
		name = name[:dot]
	}
	return 0, false
}

type handler struct {
	control *Control
	name    string
	sink    slog.Handler
}

func (h handler) Enabled(ctx context.Context, level slog.Level) bool {
	h.control.mu.RLock()
	defer h.control.mu.RUnlock()
	if threshold, ok := h.control.override(h.name); ok {
		return level >= threshold
	}
	return h.sink.Enabled(ctx, level)
}

func (h handler) Handle(ctx context.Context, record slog.Record) error {
	if err := h.sink.Handle(ctx, record); err != nil {
		return fmt.Errorf("write controlled log: %w", err)
	}
	return nil
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.sink = h.sink.WithAttrs(attrs)
	return h
}

func (h handler) WithGroup(name string) slog.Handler { h.sink = h.sink.WithGroup(name); return h }
