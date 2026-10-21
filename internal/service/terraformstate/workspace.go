package terraformstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// LatestVersion is the Terraform version of a workspace created without
// one, which Terraform's version checks accept from any client.
const LatestVersion = "latest"

// Workspace is one workspace of a repository.
type Workspace struct {
	// RecordID identifies this incarnation of workspace.json, including when
	// a workspace is deleted and recreated within the same millisecond.
	RecordID int64
	Repo     string
	Name     string
	// TerraformVersion is the version the create named, or LatestVersion.
	TerraformVersion string
	Created          time.Time
	CreatedBy        string
	// Updated is when the workspace was created or its current state
	// written, whichever is later.
	Updated time.Time
	// Lock is the lock, nil while the workspace is unlocked.
	Lock *Lock
}

// ID is the workspace's ID, "<repository key>__<name>".
func (w Workspace) ID() string { return tfbe.WorkspaceID(w.Repo, w.Name) }

// Lock is who holds a workspace's lock, and since when.
type Lock struct {
	By string
	At time.Time
}

// record is workspace.json (decision 0041, "Storage") [I]: what 7.x's
// holds is unknown, beyond a Terraform version (RTFACT-29616).
type record struct {
	Name             string `json:"name"`
	ID               string `json:"id"`
	TerraformVersion string `json:"terraform-version"` //nolint:tagliatelle // decision 0041's names
	Created          string `json:"created"`
	CreatedBy        string `json:"createdBy"` //nolint:tagliatelle // as above
}

// maxRecord bounds a read of a workspace record, which the server writes
// at a few hundred bytes.
const maxRecord = 64 << 10

// Workspaces lists c's workspaces by name.
func (s *Service) Workspaces(ctx context.Context, c repo.Config) ([]Workspace, error) {
	var out []Workspace
	for page := (store.Page{Limit: store.MaxLimit}); ; {
		children, next, err := s.store().Items().Children(ctx, c.Key, false, tfbe.Root, page)
		if errors.Is(err, store.ErrNotFound) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("terraformstate: workspaces of %s: %w", c.Key, err)
		}
		for _, f := range children {
			if !f.Folder {
				continue
			}
			w, err := s.Workspace(ctx, c, f.Name)
			if errors.Is(err, ErrNoWorkspace) {
				// A folder without a record is no workspace: one whose
				// record was deleted through the item routes.
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, w)
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

// Workspace returns workspace name of c, which exists while its record
// does.
func (s *Service) Workspace(ctx context.Context, c repo.Config, name string) (Workspace, error) {
	if tfbe.CheckName(name) != nil {
		return Workspace{}, fmt.Errorf("%w: %q", ErrNoWorkspace, name)
	}
	rec, err := s.fileAt(ctx, c, tfbe.RecordPath(name))
	if err != nil {
		return Workspace{}, err
	}
	if rec == nil {
		return Workspace{}, fmt.Errorf("%w: %s in %s", ErrNoWorkspace, name, c.Key)
	}
	body, err := s.readRecord(ctx, *rec)
	if err != nil {
		return Workspace{}, err
	}
	w := Workspace{
		RecordID: rec.ID, Repo: c.Key, Name: name, TerraformVersion: body.TerraformVersion,
		Created: rec.Created, CreatedBy: rec.CreatedBy, Updated: rec.Created,
	}
	if w.TerraformVersion == "" {
		w.TerraformVersion = LatestVersion
	}
	folder, err := s.store().Items().Get(ctx, c.Key, false, tfbe.Folder(name))
	if err != nil {
		return Workspace{}, fmt.Errorf("terraformstate: workspace folder %s: %w", name, err)
	}
	if w.Lock, err = s.lockOf(ctx, folder); err != nil {
		return Workspace{}, err
	}
	latest, err := s.fileAt(ctx, c, tfbe.LatestPath(name))
	if err != nil {
		return Workspace{}, err
	}
	if latest != nil && latest.Created.After(w.Updated) {
		w.Updated = latest.Created
	}
	return w, nil
}

func (s *Service) readRecord(ctx context.Context, it types.Item) (record, error) {
	r, _, err := s.Items.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return record{}, fmt.Errorf("terraformstate: open %s: %w", it.FullPath(), err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(io.LimitReader(r, maxRecord))
	if err != nil {
		return record{}, fmt.Errorf("terraformstate: read %s: %w", it.FullPath(), err)
	}
	var rec record
	if err := json.Unmarshal(b, &rec); err != nil {
		return record{}, fmt.Errorf("terraformstate: %s: %w", it.FullPath(), err)
	}
	return rec, nil
}

// CreateWorkspace creates workspace name in c with its record, naming
// terraformVersion, or LatestVersion when it is empty. A name that cannot
// be a folder is ErrInvalidName, and one taken ErrTaken.
func (s *Service) CreateWorkspace(
	ctx context.Context,
	c repo.Config,
	name, terraformVersion, by string,
) (Workspace, error) {
	if err := tfbe.CheckName(name); err != nil {
		return Workspace{}, err //nolint:wrapcheck // the format names it
	}
	if terraformVersion == "" {
		terraformVersion = LatestVersion
	}
	now := s.now().Truncate(time.Millisecond)
	// A struct of strings always encodes.
	body, _ := json.Marshal(record{
		Name: name, ID: tfbe.WorkspaceID(c.Key, name), TerraformVersion: terraformVersion,
		Created: now.Format(time.RFC3339Nano), CreatedBy: by,
	})
	items := s.at(now)
	info, err := items.Upload(ctx, item.Deploy{
		Target: target(c, tfbe.RecordPath(name)), Body: bytes.NewReader(body),
	})
	if err != nil {
		return Workspace{}, fmt.Errorf("terraformstate: store the record of %s: %w", name, err)
	}
	err = s.store().Tx(ctx, func(ctx context.Context) error {
		ctx = tfbe.Writing(ctx, nil)
		if _, err := items.MakeFolder(ctx, target(c, tfbe.Folder(name)), nil, by); err != nil {
			return fmt.Errorf("terraformstate: workspace folder %s: %w", name, err)
		}
		// Two creates of one name take turns here, so the second finds the
		// first's record.
		if _, err := s.holdFolder(ctx, c, name); err != nil {
			return err
		}
		_, err := items.DeployCommitted(ctx, item.Deploy{
			Target: target(c, tfbe.RecordPath(name)), By: by, Trusted: true, IfAbsent: true,
		}, info)
		if errors.Is(err, item.ErrExists) {
			return fmt.Errorf("%w: %s", ErrTaken, name)
		}
		if err != nil {
			return fmt.Errorf("terraformstate: store the record of %s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return Workspace{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return s.Workspace(ctx, c, name)
}

// DeleteWorkspace deletes workspace name of c with its states, into the
// trash can while it is on. Another user's lock refuses it with a
// *LockedError; the caller's own does not, as Terraform locks the
// workspace it deletes.
func (s *Service) DeleteWorkspace(ctx context.Context, c repo.Config, name, by string) error {
	err := s.store().Tx(ctx, func(ctx context.Context) error {
		folder, err := s.holdWorkspace(ctx, c, name)
		if err != nil {
			return err
		}
		lock, err := s.lockOf(ctx, folder)
		if err != nil {
			return err
		}
		if lock != nil && lock.By != by {
			return &LockedError{Workspace: name, Holder: lock.By}
		}
		if _, err := s.Items.Delete(ctx, target(c, tfbe.Folder(name)), by); err != nil {
			return fmt.Errorf("terraformstate: delete workspace %s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// Lock locks workspace name of c for by. Any lock refuses it with a
// *LockedError, the caller's own included, as Terraform takes a lock once.
func (s *Service) Lock(ctx context.Context, c repo.Config, name, by string) (Workspace, error) {
	return s.changeLock(ctx, c, name, func(lock *Lock) (types.Properties, []string, error) {
		if lock != nil {
			return nil, nil, &LockedError{Workspace: name, Holder: lock.By}
		}
		return types.Properties{
			tfbe.PropLockedBy: {by},
			tfbe.PropLockedAt: {strconv.FormatInt(s.now().UnixMilli(), 10)},
		}, nil, nil
	})
}

// Unlock releases by's lock on workspace name of c: a *NotLockedError when
// there is none, and a *LockedError when another user holds it.
func (s *Service) Unlock(ctx context.Context, c repo.Config, name, by string) (Workspace, error) {
	return s.changeLock(ctx, c, name, func(lock *Lock) (types.Properties, []string, error) {
		switch {
		case lock == nil:
			return nil, nil, &NotLockedError{Workspace: name}
		case lock.By != by:
			return nil, nil, &LockedError{Workspace: name, Holder: lock.By}
		}
		return nil, []string{tfbe.PropLockedBy, tfbe.PropLockedAt}, nil
	})
}

// ForceUnlock releases anyone's lock on workspace name of c: a
// *NotLockedError when there is none.
func (s *Service) ForceUnlock(ctx context.Context, c repo.Config, name string) (Workspace, error) {
	return s.changeLock(ctx, c, name, func(lock *Lock) (types.Properties, []string, error) {
		if lock == nil {
			return nil, nil, &NotLockedError{Workspace: name}
		}
		return nil, []string{tfbe.PropLockedBy, tfbe.PropLockedAt}, nil
	})
}

// changeLock reads the lock of workspace name and changes the folder's
// properties as change says, in one transaction that holds the folder, so
// of two concurrent changes the second sees the first's result. The
// properties are written to the store directly: the format refuses them
// to every other writer.
func (s *Service) changeLock(
	ctx context.Context,
	c repo.Config,
	name string,
	change func(*Lock) (set types.Properties, remove []string, err error),
) (Workspace, error) {
	err := s.store().Tx(ctx, func(ctx context.Context) error {
		folder, err := s.holdWorkspace(ctx, c, name)
		if err != nil {
			return err
		}
		lock, err := s.lockOf(ctx, folder)
		if err != nil {
			return err
		}
		set, remove, err := change(lock)
		if err != nil {
			return err
		}
		if err := s.store().
			Items().
			PatchProperties(ctx, folder.ID, set, remove, s.now()); err != nil {
			return fmt.Errorf("terraformstate: lock of %s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return Workspace{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return s.Workspace(ctx, c, name)
}

// holdWorkspace is holdFolder for an existing workspace: ErrNoWorkspace
// when it has no record.
func (s *Service) holdWorkspace(
	ctx context.Context,
	c repo.Config,
	name string,
) (types.Item, error) {
	folder, err := s.holdFolder(ctx, c, name)
	if err != nil {
		return types.Item{}, err
	}
	rec, err := s.fileAt(ctx, c, tfbe.RecordPath(name))
	if err != nil {
		return types.Item{}, err
	}
	if rec == nil {
		return types.Item{}, fmt.Errorf("%w: %s in %s", ErrNoWorkspace, name, c.Key)
	}
	return folder, nil
}

// holdFolder returns the folder of workspace name, held until ctx's
// transaction ends. It writes the folder back unchanged: on PostgreSQL that
// takes the row's lock, so a second transaction waits here and then reads
// what the first committed, which a read alone would not, under READ
// COMMITTED. SQLite and the in-memory store run one writer at a time
// anyway.
func (s *Service) holdFolder(ctx context.Context, c repo.Config, name string) (types.Item, error) {
	if tfbe.CheckName(name) != nil {
		return types.Item{}, fmt.Errorf("%w: %q", ErrNoWorkspace, name)
	}
	items := s.store().Items()
	folder, err := items.Get(ctx, c.Key, false, tfbe.Folder(name))
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && !folder.Folder:
		return types.Item{}, fmt.Errorf("%w: %s in %s", ErrNoWorkspace, name, c.Key)
	case err != nil:
		return types.Item{}, fmt.Errorf("terraformstate: workspace folder %s: %w", name, err)
	}
	if folder, err = items.Put(ctx, folder); err != nil {
		return types.Item{}, fmt.Errorf("terraformstate: hold workspace folder %s: %w", name, err)
	}
	return folder, nil
}

// lockOf reads the lock on a workspace's folder; nil when it has none.
func (s *Service) lockOf(ctx context.Context, folder types.Item) (*Lock, error) {
	props, err := s.store().Items().Properties(ctx, folder.ID)
	if err != nil {
		return nil, fmt.Errorf("terraformstate: lock of %s: %w", folder.FullPath(), err)
	}
	by := props[tfbe.PropLockedBy]
	if len(by) == 0 {
		return nil, nil //nolint:nilnil // unlocked is no failure
	}
	lock := &Lock{By: by[0]}
	if at := props[tfbe.PropLockedAt]; len(at) > 0 {
		if ms, err := strconv.ParseInt(at[0], 10, 64); err == nil {
			lock.At = time.UnixMilli(ms).UTC()
		}
	}
	return lock, nil
}
