package configurationhttp

import (
	"context"
	"errors"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// SetLoggerDebugLevels starts one bounded in-memory session over actual loggers.
func (s SystemAPI) SetLoggerDebugLevels(ctx context.Context, req artgen.SetLoggerDebugLevelsRequestObject) (artgen.SetLoggerDebugLevelsResponseObject, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err
	}
	if s.deps.Configuration.Loggers == nil {
		return nil, apierror.ErrNotImplemented
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("logger request is required")
	}
	classes := make(map[string]string, len(req.Body.Classes))
	for name, level := range req.Body.Classes {
		classes[name] = string(level)
	}
	if err := s.deps.Configuration.Loggers.Set(ctx, classes, req.Body.Timer); err != nil {
		return nil, s.loggerError(ctx, err)
	}
	return artgen.SetLoggerDebugLevels200JSONResponse("Logger levels configured successfully"), nil
}

// DeleteLoggerDebugLevels restores the original structured handler's decisions.
func (s SystemAPI) DeleteLoggerDebugLevels(ctx context.Context, _ artgen.DeleteLoggerDebugLevelsRequestObject) (artgen.DeleteLoggerDebugLevelsResponseObject, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err
	}
	if s.deps.Configuration.Loggers == nil {
		return nil, apierror.ErrNotImplemented
	}
	if err := s.deps.Configuration.Loggers.Delete(ctx); err != nil {
		return nil, s.loggerError(ctx, err)
	}
	return artgen.DeleteLoggerDebugLevels200JSONResponse("Logger levels were reset to default settings successfully"), nil
}

// GetLoggerDebugLevel reports the level currently used for a registered logger.
func (s SystemAPI) GetLoggerDebugLevel(ctx context.Context, req artgen.GetLoggerDebugLevelRequestObject) (artgen.GetLoggerDebugLevelResponseObject, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return nil, err
	}
	if s.deps.Configuration.Loggers == nil {
		return nil, apierror.ErrNotImplemented
	}
	level, err := s.deps.Configuration.Loggers.Level(ctx, req.ClassName)
	if err != nil {
		return nil, s.loggerError(ctx, err)
	}
	return artgen.GetLoggerDebugLevel200JSONResponse(level), nil
}

func (s SystemAPI) loggerError(ctx context.Context, err error) error {
	if errors.Is(err, configapi.ErrInvalidLoggerSettings) {
		return httpkit.BadRequestf("%s", err)
	}
	var typed *apierror.Error
	if errors.As(err, &typed) && typed.Status < 500 || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	return s.internalError(ctx, "temporary logger controls", err)
}
