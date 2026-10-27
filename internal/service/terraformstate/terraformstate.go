// Package terraformstate is the Terraform backend's storage (decision
// 0041): the workspaces, state versions and locks of terraformbackend
// repositories. They are items under workspaces/<ws>/, written through the
// item service, so the format's hooks (internal/format/terraformbackend)
// index them, a delete puts them in the trash can, and the storage API, AQL
// and replication see them as any other item. A state is sealed under the
// master key while the repository's encryptStates is on.
//
// The TFE-shaped API that Terraform's remote backend calls is built on it.
// It checks no permission: the API does, on the paths terraformbackend
// names (Folder, RecordPath, LatestPath), before each call.
package terraformstate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Errors the API maps onto statuses. The three that wrap store.ErrNotFound
// answer 404.
var (
	// ErrNoRepository is a key that names no local terraformbackend
	// repository: no organisation.
	ErrNoRepository = fmt.Errorf("terraformstate: no local terraform backend repository: %w",
		store.ErrNotFound)
	// ErrNoWorkspace is a workspace that does not exist.
	ErrNoWorkspace = fmt.Errorf("terraformstate: no such workspace: %w", store.ErrNotFound)
	// ErrNoState is a state version that is not stored, or a workspace
	// without a current state.
	ErrNoState = fmt.Errorf("terraformstate: no such state version: %w", store.ErrNotFound)
	// ErrInvalidName is a workspace name that cannot name a folder (422).
	ErrInvalidName = terraformbackend.ErrInvalidName
	// ErrTaken is a workspace create for a name that exists (422).
	ErrTaken = errors.New("terraformstate: the workspace name is taken")
	// ErrWritten is a write of a state version that is stored already (409).
	ErrWritten = errors.New("terraformstate: the state version is written already")
	// ErrWorkspaceChanged refuses a capability for an earlier workspace record.
	ErrWorkspaceChanged = errors.New("terraformstate: the workspace was replaced")
	// ErrNoKeyring is a state to seal or open without a master key.
	ErrNoKeyring = terraformbackend.ErrNoKeyring
)

// LockedError is a workspace whose lock refuses the caller: any holder's
// for a lock, and another user's for an unlock, a state write or a delete.
// Its text is the one go-tfe reads: "is locked by User" turns a refused
// unlock into "unable to unlock workspace locked by user".
type LockedError struct {
	Workspace, Holder string
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("Workspace %s is locked by User %s", e.Workspace, e.Holder)
}

// NotLockedError is an unlock or a force-unlock of a workspace no one has
// locked.
type NotLockedError struct {
	Workspace string
}

func (e *NotLockedError) Error() string {
	return fmt.Sprintf("Workspace %s is not locked", e.Workspace)
}

// InvalidStateError is a state write whose content is no Terraform state
// or differs from what the request declared (422).
type InvalidStateError struct {
	Message string
}

func (e *InvalidStateError) Error() string { return e.Message }

// Service keeps the Terraform backend's workspaces and states.
type Service struct {
	// Items writes and deletes the items. Its formats must give the
	// Terraform backend's the same Keys, so the hooks read what this
	// seals.
	Items *item.Service
	// Keys seals states while encryptStates is on and opens them. Without
	// it such a write fails, and a sealed state does not open.
	Keys *crypt.Keyring
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) store() store.Store { return s.Items.Store }

// at is the item service with its clock stopped at t, so what a write
// stores is created at t: a state version's milliseconds name it, and the
// current state's creation time is how a reindex reads them back.
func (s *Service) at(t time.Time) *item.Service {
	items := *s.Items
	items.Now = func() time.Time { return t }
	return &items
}

// Repository returns the local terraformbackend repository key names: the
// organisation of decision 0041. Any other key is ErrNoRepository.
func (s *Service) Repository(ctx context.Context, key string) (repo.Config, error) {
	c, err := s.store().Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return repo.Config{}, fmt.Errorf("%w: %s", ErrNoRepository, key)
	case err != nil:
		return repo.Config{}, fmt.Errorf("terraformstate: get repository %s: %w", key, err)
	case !terraformbackend.Serves(c.PackageType) || c.Class != repo.Local:
		return repo.Config{}, fmt.Errorf("%w: %s", ErrNoRepository, key)
	}
	return c, nil
}

// ResolveWorkspace reads a workspace ID, "<repository key>__<workspace
// name>": the first split from the left whose repository holds a workspace
// of that name (decision 0041, "Identifiers") [I].
func (s *Service) ResolveWorkspace(ctx context.Context, id string) (repo.Config, Workspace, error) {
	for _, split := range terraformbackend.Splits(id) {
		c, err := s.Repository(ctx, split[0])
		if errors.Is(err, ErrNoRepository) {
			continue
		}
		if err != nil {
			return repo.Config{}, Workspace{}, err
		}
		w, err := s.Workspace(ctx, c, split[1])
		if errors.Is(err, ErrNoWorkspace) {
			continue
		}
		if err != nil {
			return repo.Config{}, Workspace{}, err
		}
		return c, w, nil
	}
	return repo.Config{}, Workspace{}, fmt.Errorf("%w: %s", ErrNoWorkspace, id)
}

// ResolveState reads a state version ID, "<workspace ID>__<ms>".
func (s *Service) ResolveState(ctx context.Context, id string) (repo.Config, State, error) {
	wsID, ms, ok := terraformbackend.SplitStateID(id)
	if !ok {
		return repo.Config{}, State{}, fmt.Errorf("%w: %s", ErrNoState, id)
	}
	c, w, err := s.ResolveWorkspace(ctx, wsID)
	if errors.Is(err, ErrNoWorkspace) {
		return repo.Config{}, State{}, fmt.Errorf("%w: %s", ErrNoState, id)
	}
	if err != nil {
		return repo.Config{}, State{}, err
	}
	st, err := s.State(ctx, c, w.Name, ms)
	return c, st, err
}

// fileAt is the file at p in c, or nil when there is none.
func (s *Service) fileAt(ctx context.Context, c repo.Config, p string) (*types.Item, error) {
	it, err := s.store().Items().Get(ctx, c.Key, false, p)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && it.Folder:
		return nil, nil //nolint:nilnil // nothing there is no failure
	case err != nil:
		return nil, fmt.Errorf("terraformstate: get %s: %w", p, err)
	}
	return &it, nil
}

// target is the item service's target of path p in c.
func target(c repo.Config, p string) item.Target { return item.Target{Repo: c, Path: p} }
