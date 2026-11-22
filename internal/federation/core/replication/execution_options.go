package replication

import (
	"context"
	"fmt"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ExecutionInput distinguishes an absent execute body from a supplied one-shot
// object. Selection defaults belong to the run, never the saved configuration.
type ExecutionInput struct {
	URL, Username, Password, Proxy, Include, Exclude                *string
	SyncDeletes, SyncProperties, DisableProxy, CheckBinaryExistence *bool
}

// WithExecutionOptions returns a driver-local configuration view. WithTarget
// still checks the actual saved target, current authority and global blocks
// before applying these immutable transfer values.
func (s Service) WithExecutionOptions(options *model.ExecutionOptions) Service {
	s.executionOptions = nil
	if options != nil {
		copy := *options
		s.executionOptions = &copy
	}
	return s
}

func (s Service) resolveExecutionOptions(ctx context.Context, repository artifactsapi.ProjectRepository,
	target model.Replication, input ExecutionInput,
) (model.ExecutionOptions, error) {
	if input.CheckBinaryExistence != nil && *input.CheckBinaryExistence {
		return model.ExecutionOptions{}, ErrAdvancedConfiguration
	}
	// An object supplies its documented selection defaults. No object uses the
	// saved configuration instead; neither path writes configuration state.
	next := target
	next.SyncDeletes, next.SyncProperties = false, true
	next.IncludePathPrefix, next.ExcludePathPrefix = "", ""
	in := Input{
		SyncDeletes: input.SyncDeletes, SyncProperties: input.SyncProperties,
		IncludePathPrefix: input.Include, ExcludePathPrefix: input.Exclude,
	}
	direction, _ := model.DirectionFor(repository.Class)
	if direction == model.ReplicationPush {
		// A different destination needs its own admission/status lifetime. Do
		// not silently route it through the saved target's public identity.
		if input.URL != nil && *input.URL != target.URL {
			return model.ExecutionOptions{}, ErrAdvancedConfiguration
		}
		in.URL, in.Username, in.Password = input.URL, input.Username, input.Password
		in.Proxy, in.DisableProxy = input.Proxy, input.DisableProxy
		if input.Username != nil && *input.Username != target.Username && input.Password == nil {
			next.Password = "" // A changed account cannot inherit its predecessor's secret.
		}
	}
	if err := s.merge(ctx, repository, target, &next, in); err != nil {
		return model.ExecutionOptions{}, fmt.Errorf("validate execution options: %w", err)
	}
	if _, err := newPushScope("", next.IncludePathPrefix, next.ExcludePathPrefix); err != nil {
		return model.ExecutionOptions{}, err
	}
	options := model.ExecutionOptions{
		URL: next.URL, Username: next.Username, Password: next.Password,
		Proxy: next.Proxy, DisableProxy: next.DisableProxy, SocketTimeout: next.SocketTimeout,
		Include: next.IncludePathPrefix, Exclude: next.ExcludePathPrefix,
		SyncDeletes: next.SyncDeletes, SyncProperties: next.SyncProperties,
	}
	if err := options.Validate(); err != nil {
		return model.ExecutionOptions{}, fmt.Errorf("validate resolved execution options: %w", err)
	}
	return options, nil
}

func executionOptions(ctx context.Context, run model.ExecutionRun, progress interface {
	Options(context.Context, model.ExecutionReference) (model.ExecutionOptions, error)
},
) (*model.ExecutionOptions, error) {
	if !run.Binding.OneShot {
		return nil, nil //nolint:nilnil // A saved-config execution intentionally has no override.
	}
	if progress == nil {
		return nil, fmt.Errorf("execution options store unavailable: %w", kernel.ErrStore)
	}
	options, err := progress.Options(ctx, run.Reference)
	if err != nil {
		return nil, fmt.Errorf("restore immutable execution options: %w", err)
	}
	return &options, nil
}
