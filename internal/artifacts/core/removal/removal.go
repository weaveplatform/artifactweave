package removal

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Authorize acquires the project guard before restoring the original verified
// credential and evaluating its current permissions. It performs no network I/O.
type Authorize func(context.Context, kernel.CredentialReference) (kernel.Principal, accessapi.Decider, error)

type remover struct {
	store     item.Store
	items     *item.Service
	authorize Authorize
}

// New binds normal deletion and current authority without performing I/O.
func New(store item.Store, items *item.Service, authorize Authorize) api.ItemRemoval {
	return remover{store: store, items: items, authorize: authorize}
}

func validate(req api.RemovalRequest) error {
	f, c := req.Item, req.Credential
	r := f.Version.Package.Repository
	if c.Name == "" || c.Anonymous || c.Registry != nil || !c.Password && c.TokenID == "" {
		return fmt.Errorf("removal requires an original authenticated credential: %w", kernel.ErrBadCredentials)
	}
	if f.ID <= 0 || f.Version.ID <= 0 || f.Version.Package.ID <= 0 || r.Key == "" ||
		f.Path == "" || f.Path != strings.Trim(f.Path, "/") || f.Size < 0 || f.Cached ||
		(r.Class != repo.Local && r.Class != repo.Federated) || r.HasFederatedMembers {
		return fmt.Errorf("removal requires an indexed local file: %w", kernel.ErrInvalid)
	}
	if _, _, _, err := kernel.SplitItemPath(f.Path); err != nil {
		return fmt.Errorf("removal item path: %w", err)
	}
	for name := range req.Properties {
		if err := model.ValidatePropertyKey(name); err != nil {
			return fmt.Errorf("removal property name: %w: %w", kernel.ErrInvalid, err)
		}
	}
	for name := range f.Properties {
		if _, ok := req.Properties[name]; !ok {
			return fmt.Errorf("removal snapshot omits a property selector: %w", kernel.ErrInvalid)
		}
	}
	return nil
}

func (r remover) Remove(
	ctx context.Context,
	req api.RemovalRequest,
	revalidate func(context.Context) error,
	record func(context.Context, api.RemovalResult) error,
) (api.RemovalResult, error) {
	if err := validate(req); err != nil {
		return api.RemovalResult{}, err
	}
	if r.store == nil || r.items == nil || r.authorize == nil || revalidate == nil || record == nil {
		return api.RemovalResult{}, fmt.Errorf("guarded removal is unavailable: %w", kernel.ErrStore)
	}
	var out api.RemovalResult
	err := r.store.Tx(ctx, func(ctx context.Context) error {
		principal, decider, err := r.authorize(ctx, req.Credential)
		if err != nil {
			return fmt.Errorf("current removal caller: %w", err)
		}
		if decider == nil || principal.Anonymous || principal.Name != req.Credential.Name {
			return kernel.ErrForbidden
		}
		ctx = accessapi.WithCaller(ctx, principal, decider)
		expected := req.Item.Version.Package.Repository
		allowed := func(ctx context.Context, path string, folder bool, want accessapi.Actions) error {
			// The guard fences credential records, but not passage of time.
			// Refresh after selection/item waits, including each companion check.
			current, permissions, err := r.authorize(ctx, req.Credential)
			if err != nil {
				return fmt.Errorf("current removal permission: %w", err)
			}
			if permissions == nil || current.Anonymous || current.Name != principal.Name ||
				folder || want != accessapi.ActionDelete || !permissions.Allows(accessapi.ResourceArtifact,
				accessapi.Repo{Key: expected.Key, Class: string(expected.Class)}, path, accessapi.ActionDelete) {
				return kernel.ErrForbidden
			}
			return nil
		}
		if err := allowed(ctx, req.Item.Path, false, accessapi.ActionDelete); err != nil {
			return err
		}
		configuration, err := r.repository(ctx, expected)
		if err != nil {
			return err
		}
		if err := r.current(ctx, req); err != nil {
			return err
		}
		if err := revalidate(ctx); err != nil {
			return fmt.Errorf("current removal selection: %w", err)
		}
		ctx = artifactevents.WithActor(ctx, principal.Name)
		target := item.Target{Repo: configuration, Path: req.Item.Path, CheckCurrent: allowed}
		var result port.DeleteResult
		if req.SkipTrashcan {
			result, err = r.items.Purge(ctx, target)
		} else {
			result, err = r.items.Delete(ctx, target, principal.Name)
		}
		if err != nil {
			return fmt.Errorf("remove selected file: %w", err)
		}
		out = api.RemovalResult{Files: result.Files, Bytes: result.Bytes, Items: make([]api.RemovedItem, 0, len(result.Removed))}
		for _, removed := range result.Removed {
			if removed.Folder {
				return fmt.Errorf("indexed removal reached a folder: %w", kernel.ErrStore)
			}
			out.Items = append(out.Items, api.RemovedItem{ID: removed.ID, Path: removed.FullPath(), Size: removed.Size, SHA256: removed.Actual.SHA256})
		}
		if err := record(ctx, api.RemovalResult{Files: out.Files, Bytes: out.Bytes, Items: slices.Clone(out.Items)}); err != nil {
			return fmt.Errorf("record selected removal: %w", err)
		}
		return nil
	})
	if err != nil {
		return api.RemovalResult{}, fmt.Errorf("guarded indexed removal: %w", err)
	}
	return out, nil
}

func (r remover) repository(ctx context.Context, expected api.IndexRepository) (repo.Config, error) {
	reader, ok := r.store.Repos().(port.RepositoryIndexPages)
	if !ok {
		return repo.Config{}, fmt.Errorf("removal repository projection unavailable: %w", kernel.ErrStore)
	}
	current, err := reader.IndexRepository(ctx, expected.Key)
	if err != nil {
		return repo.Config{}, fmt.Errorf("current removal repository: %w", err)
	}
	if current.Key != expected.Key || current.Project != expected.Project || current.Class != expected.Class ||
		current.PackageType != expected.PackageType || current.HasFederatedMembers != expected.HasFederatedMembers ||
		!slices.Equal(current.Environments, expected.Environments) {
		return repo.Config{}, fmt.Errorf("removal repository changed: %w", kernel.ErrConflict)
	}
	c, err := r.store.Repos().Get(ctx, expected.Key)
	if err != nil {
		return repo.Config{}, fmt.Errorf("removal repository configuration: %w", err)
	}
	return c, nil
}

func (r remover) current(ctx context.Context, req api.RemovalRequest) error {
	expected, items := req.Item, r.store.Items()
	// Properties takes the item guard. Refresh the row afterwards because the
	// preceding lookup may have waited while an overwrite committed.
	if _, err := items.GetByID(ctx, expected.ID); err != nil {
		return fmt.Errorf("selected removal item: %w", err)
	}
	properties, err := items.Properties(ctx, expected.ID)
	if err != nil {
		return fmt.Errorf("selected removal properties: %w", err)
	}
	current, err := items.GetByID(ctx, expected.ID)
	if err != nil {
		return fmt.Errorf("current removal item: %w", err)
	}
	stats, err := items.Stats(ctx, expected.ID)
	if err != nil {
		return fmt.Errorf("current removal downloads: %w", err)
	}
	if current.Folder || current.Cached != expected.Cached || current.RepoKey != expected.Version.Package.Repository.Key ||
		current.FullPath() != expected.Path || current.Size != expected.Size || current.Actual != expected.Actual ||
		!current.Created.Equal(expected.Created) || !current.Modified.Equal(expected.Modified) || !current.Updated.Equal(expected.Updated) ||
		!current.PropertiesModified.Equal(expected.PropertiesModified) || !stats.LastDownloaded.Equal(expected.LastDownloaded) {
		return fmt.Errorf("selected removal item changed: %w", kernel.ErrConflict)
	}
	selected := make(map[string][]string, len(req.Properties))
	for name, wanted := range req.Properties {
		for _, value := range properties[name] {
			if slices.Contains(wanted, value) {
				selected[name] = append(selected[name], value)
			}
		}
	}
	if !maps.EqualFunc(selected, expected.Properties, slices.Equal[[]string]) {
		return fmt.Errorf("selected removal property values changed: %w", kernel.ErrConflict)
	}
	return nil
}
