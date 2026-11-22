package configurationhttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type loggerPort struct {
	calls   int
	failure error
}

func (p *loggerPort) Set(context.Context, map[string]string, int) error { p.calls++; return p.failure }

func (p *loggerPort) Delete(context.Context) error { p.calls++; return p.failure }

func (p *loggerPort) Level(context.Context, string) (string, error) {
	p.calls++
	return "debug", p.failure
}

func TestUnitLoggerHandlersAuthorizeBeforeAccess(t *testing.T) {
	port := &loggerPort{}
	server := New(Deps{Configuration: configapi.API{Loggers: port}})
	request := artgen.SetLoggerDebugLevelsRequestObject{
		Body: &artgen.SetLoggerDebugLevelsJSONRequestBody{Classes: map[string]artgen.LoggerLevel{"artifactweave.http": artgen.LoggerLevelDebug}, Timer: 60},
	}
	for _, ctx := range []context.Context{t.Context(), kernel.WithPrincipal(t.Context(), kernel.Anonymous), kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "user"})} {
		if _, err := server.SetLoggerDebugLevels(ctx, request); err == nil {
			t.Fatal("unprivileged POST admitted")
		}
		if _, err := server.DeleteLoggerDebugLevels(ctx, artgen.DeleteLoggerDebugLevelsRequestObject{}); err == nil {
			t.Fatal("unprivileged DELETE admitted")
		}
		if _, err := server.GetLoggerDebugLevel(ctx, artgen.GetLoggerDebugLevelRequestObject{ClassName: "artifactweave.http"}); err == nil {
			t.Fatal("unprivileged GET admitted")
		}
	}
	if port.calls != 0 {
		t.Fatal("unauthorized handler reached logger controls")
	}
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	{
		s := New(Deps{})
		if _, err := s.SetLoggerDebugLevels(ctx, request); !errors.Is(err, apierror.ErrNotImplemented) {
			t.Fatal(err)
		}
		if _, err := s.DeleteLoggerDebugLevels(ctx, artgen.DeleteLoggerDebugLevelsRequestObject{}); !errors.Is(err, apierror.ErrNotImplemented) {
			t.Fatal(err)
		}
		if _, err := s.GetLoggerDebugLevel(ctx, artgen.GetLoggerDebugLevelRequestObject{}); !errors.Is(err, apierror.ErrNotImplemented) {
			t.Fatal(err)
		}
	}
	if _, err := server.SetLoggerDebugLevels(ctx, artgen.SetLoggerDebugLevelsRequestObject{}); err == nil {
		t.Fatal("nil body accepted")
	}
	if _, err := server.SetLoggerDebugLevels(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := server.DeleteLoggerDebugLevels(ctx, artgen.DeleteLoggerDebugLevelsRequestObject{}); err != nil {
		t.Fatal(err)
	}
	got, err := server.GetLoggerDebugLevel(ctx, artgen.GetLoggerDebugLevelRequestObject{})
	if err != nil || got != artgen.GetLoggerDebugLevel200JSONResponse("debug") {
		t.Fatal(got, err)
	}
	for _, cause := range []error{configapi.ErrInvalidLoggerSettings, kernel.ErrForbidden, kernel.ErrBadCredentials, kernel.ErrAccountLocked, context.Canceled, context.DeadlineExceeded, kernel.ErrStore} {
		port.failure = cause
		if _, err := server.SetLoggerDebugLevels(ctx, request); err == nil {
			t.Fatal("POST dropped failure")
		}
		if _, err := server.DeleteLoggerDebugLevels(ctx, artgen.DeleteLoggerDebugLevelsRequestObject{}); err == nil {
			t.Fatal("DELETE dropped failure")
		}
		if _, err := server.GetLoggerDebugLevel(ctx, artgen.GetLoggerDebugLevelRequestObject{}); err == nil {
			t.Fatal("GET dropped failure")
		}
	}
}

func TestUnitLoggerErrorsKeepClassification(t *testing.T) {
	server := New(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	for _, cause := range []error{kernel.ErrForbidden, kernel.ErrBadCredentials, kernel.ErrAccountLocked, context.Canceled, context.DeadlineExceeded} {
		if err := server.loggerError(t.Context(), cause); !errors.Is(err, cause) {
			t.Fatal("typed refusal changed", err)
		}
	}
	for _, entry := range []struct {
		name    string
		cause   error
		status  int
		message string
	}{{"invalid logger settings", configapi.ErrInvalidLoggerSettings, http.StatusBadRequest, "invalid logger settings"}, {"store failure", kernel.ErrStore, http.StatusInternalServerError, "internal error"}} {
		t.Run(entry.name, func(t *testing.T) {
			var typed *apierror.Error
			if err := server.loggerError(t.Context(), entry.cause); !errors.As(err, &typed) || typed.Status != entry.status {
				t.Fatal(err)
			}
			if typed.Message != entry.message {
				t.Fatalf("logger error = %q; want %q", typed.Message, entry.message)
			}
		})
	}
}
