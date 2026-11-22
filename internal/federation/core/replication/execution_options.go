package replication

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
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

// canonicalExecutionURL names the actual repository endpoint independently of
// credentials, proxy and equivalent URL spelling, without DNS or peer I/O.
func canonicalExecutionURL(raw string) (string, error) {
	endpoint, err := NewPushEndpoint(raw)
	if err != nil {
		return "", err
	}
	address, err := endpoint.route(endpoint.prefix, endpoint.repository)
	if err != nil {
		return "", err
	}
	address.Scheme = strings.ToLower(address.Scheme)
	host, port := strings.ToLower(address.Hostname()), address.Port()
	defaultPort := address.Scheme == "http" && port == "80" || address.Scheme == "https" && port == "443"
	if port != "" && !defaultPort {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	address.Host = host
	return address.String(), nil
}

func (c Coordinator) executionTarget(ctx context.Context, repository artifactsapi.ProjectRepository, input *ExecutionInput) (model.Replication, *model.ExecutionOptions, error) {
	independent := input != nil && input.URL != nil && repository.Class == repo.Local
	var target model.Replication
	if independent {
		url, err := canonicalExecutionURL(*input.URL)
		if err != nil {
			return target, nil, err
		}
		copy := *input
		copy.URL = &url
		input = &copy
		// Explicit connections never inherit a saved target's credentials.
		target = model.Replication{
			RepoKey: repository.Key, Key: "explicit:" + ownershipHash([]string{repository.Key, url}),
			Revision: rand.Text(), Enabled: true, SocketTimeout: 15 * time.Second,
		}
	} else {
		var err error
		target, err = c.Configurations.target(ctx, repository.Key)
		if err != nil {
			return target, nil, err
		}
	}
	if input == nil {
		return target, nil, nil
	}
	options, err := c.Configurations.resolveExecutionOptions(ctx, repository, target, *input)
	if err != nil {
		return model.Replication{}, nil, err
	}
	if independent {
		options.Independent, options.RepositoryKey, options.TargetKey, options.TargetRevision = true, target.RepoKey, target.Key, target.Revision
	}
	return target, &options, nil
}

func (s Service) withExplicitTarget(ctx context.Context, key, revision string, fn func(context.Context, model.Replication) error) error {
	options := *s.executionOptions
	if options.Validate() != nil || key != options.TargetKey || revision != options.TargetRevision {
		return fmt.Errorf("explicit replication target changed: %w", kernel.ErrConflict)
	}
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Guard(ctx); err != nil {
			return fmt.Errorf("guard explicit replication: %w", err)
		}
		repository, err := s.Ownership.ProjectRepository(ctx, options.RepositoryKey)
		if err != nil {
			return fmt.Errorf("read explicit replication repository: %w", err)
		}
		if repository.Class != repo.Local {
			return kernel.ErrConflict
		}
		blocks, err := s.blocks(ctx)
		if err != nil {
			return err
		}
		if blocks.Push {
			return kernel.ErrConflict
		}
		target := options.Apply(model.Replication{RepoKey: options.RepositoryKey, Key: key, Revision: revision, Enabled: true})
		return fn(ctx, target)
	})
	if err != nil {
		return fmt.Errorf("explicit replication admission: %w", err)
	}
	return nil
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
	if err := options.ValidateBinding(run); err != nil {
		return nil, fmt.Errorf("restore bound execution options: %w", err)
	}
	return &options, nil
}
