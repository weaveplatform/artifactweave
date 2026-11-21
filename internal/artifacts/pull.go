package artifacts

import (
	"context"
	"fmt"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/peerstorage"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (m *Module) readPullContent(ctx context.Context, request source.Request) (source.Result, error) {
	service := m.remoteSources
	if docker.Serves(request.Repo.PackageType) {
		service = m.registrySources
	}
	if service == nil {
		return source.Result{}, source.ErrUnavailable
	}
	return service.Read(remote.WithSmart(ctx, false), request) //nolint:wrapcheck // retain the ordinary P9 refusal
}

func openPullPeer(ctx context.Context, service *source.Service, request source.Request, root string, scope remote.DiscoveryScope) (pull.Peer, error) {
	reader, err := peerstorage.New(ctx, service, request, root, scope)
	if err != nil {
		return nil, fmt.Errorf("open ordinary pull storage: %w", err)
	}
	return reader, nil
}

func (m *Module) currentPullSource(ctx context.Context, key, root string) (*source.Service, source.Request, error) {
	// Resolve the runtime pointer here, after OpenRemoteSources. Capturing it
	// while New binds API would permanently capture its not-yet-open nil value.
	if m.remoteSources == nil {
		return nil, source.Request{}, source.ErrUnavailable
	}
	if strings.HasSuffix(key, model.CacheSuffix) {
		return nil, source.Request{}, source.ErrInvalidRequest
	}
	var request source.Request
	err := m.store.Tx(ctx, func(tx context.Context) error {
		if err := m.guardAuthority(tx); err != nil {
			return err
		}
		principal, decider, err := m.currentContentCaller(tx)
		if err != nil {
			return err
		}
		metadata, err := m.store.Repos().ProjectRepository(tx, key)
		if err != nil {
			return fmt.Errorf("owning pull repository: %w", err)
		}
		if decider == nil || !decider.AllowsFolder(accessapi.ResourceArtifact,
			accessapi.Repo{Key: key, Class: string(metadata.Class)}, root, accessapi.ActionRead) {
			return kernel.ErrForbidden
		}
		if metadata.Class != repo.Remote {
			return source.ErrInvalidRequest
		}
		config, err := m.store.Repos().Get(tx, key)
		if err != nil {
			return fmt.Errorf("current pull configuration: %w", err)
		}
		request = source.Request{Repo: config, Subject: accessapi.SubjectOf(principal), Access: decider, Project: config.ProjectKey}
		return nil
	})
	if err != nil {
		return nil, source.Request{}, fmt.Errorf("current owning pull source: %w", err)
	}
	return m.remoteSources, request, nil
}
