package logging_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/logging"
)

func TestUnitLevelsChangeRealRecordsAndPreserveSink(t *testing.T) {
	var output bytes.Buffer
	var baseline slog.LevelVar
	baseline.Set(slog.LevelWarn)
	control := logging.New(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{
		Level: &baseline, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == "password" {
				a.Value = slog.StringValue("[redacted]")
			}
			return a
		},
	})))
	root := control.Logger("artifactweave")
	child := control.Logger("artifactweave.remote")
	grandchild := control.Logger("artifactweave.remote.http")
	grouped := child.With("identity", "fixture").WithGroup("details").With("password", "fixture-secret")
	ctx := t.Context()
	grouped.DebugContext(ctx, "before")
	if output.Len() != 0 {
		t.Fatal("debug emitted before override")
	}
	if err := control.Change(
		map[string]slog.Level{"artifactweave": slog.LevelInfo, "artifactweave.remote": slog.LevelDebug, "unknown": slog.LevelError},
		func() error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	if root.Enabled(ctx, slog.LevelDebug) || !child.Enabled(ctx, slog.LevelDebug) || !grandchild.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("child specificity did not control real decisions")
	}
	grouped.DebugContext(ctx, "during")
	if !strings.Contains(output.String(), `"msg":"during"`) || !strings.Contains(output.String(), `"logger":"artifactweave.remote"`) ||
		!strings.Contains(output.String(), `"details":{`) ||
		strings.Contains(output.String(), "fixture-secret") {
		t.Fatalf("structured sink/redaction lost: %s", output.String())
	}
	if got := control.Level(ctx, "unknown"); got != slog.LevelInfo {
		t.Fatalf("unknown logger = %v", got)
	}
	marker := errors.New("failed publication")
	if err := control.Change(nil, func() error { return marker }); !errors.Is(err, marker) || control.Level(ctx, "artifactweave.remote") != slog.LevelDebug {
		t.Fatal("failed publication changed levels", err)
	}
	baseline.Set(slog.LevelError)
	control.Clear()
	if root.Enabled(ctx, slog.LevelWarn) || child.Enabled(ctx, slog.LevelWarn) || control.Level(ctx, "artifactweave.remote") != slog.LevelError {
		t.Fatal("clear restored stale baseline")
	}
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn} {
		baseline.Set(level)
		if got := control.Level(ctx, "unknown"); got != level {
			t.Errorf("root baseline = %v; want %v", got, level)
		}
	}
}

func TestUnitLevelsConcurrentReadersAndRegistration(t *testing.T) {
	control := logging.New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 100 {
				logger := control.Logger("artifactweave.remote")
				logger.InfoContext(t.Context(), "record")
				_ = control.Level(t.Context(), "artifactweave.remote")
			}
		})
	}
	for range 100 {
		if err := control.Change(map[string]slog.Level{"artifactweave": slog.LevelWarn}, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		control.Clear()
	}
	group.Wait()
}

type brokenSink struct {
	slog.Handler
	cause error
}

func (h brokenSink) Handle(context.Context, slog.Record) error { return h.cause }
func (h brokenSink) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h brokenSink) WithGroup(string) slog.Handler             { return h }

func TestUnitLevelsPreserveHandlerErrorsAndDefault(t *testing.T) {
	marker := errors.New("sink failed")
	control := logging.New(slog.New(brokenSink{Handler: slog.DiscardHandler, cause: marker}))
	if err := control.Logger("artifactweave").Handler().Handle(t.Context(), slog.Record{}); !errors.Is(err, marker) {
		t.Fatal("sink error lost", err)
	}
	if got := logging.New(nil).Level(t.Context(), "unknown"); got != slog.LevelInfo {
		t.Fatal("nil logger root", got)
	}
}
