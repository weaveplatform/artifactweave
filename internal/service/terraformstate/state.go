package terraformstate

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// State is one state version of a workspace.
type State struct {
	Repo, Workspace string
	// Ms names the version: the epoch milliseconds at which it was created.
	Ms int64
	// Current marks the workspace's current state, state.latest.json.
	Current bool
	// Path is the state file's path in the repository.
	Path string
	// Meta is what the package index keeps of it: its serial, lineage, MD5
	// and size. An Unreadable state does not open under this server's
	// master key, or is no state.
	Meta      tfbe.Meta
	CreatedBy string
}

// ID is the state version's ID, "<workspace ID>__<ms>".
func (st State) ID() string { return tfbe.StateID(st.Repo, st.Workspace, st.Ms) }

// Created is when the state version was created.
func (st State) Created() time.Time { return tfbe.Created(st.Ms) }

// StateWrite is a state to store as a workspace's current state.
type StateWrite struct {
	// Ms is the version's milliseconds when an earlier request named it, as
	// a create that returned an upload URL does (NewState); zero takes the
	// next.
	Ms int64
	// Body is the raw state.
	Body io.Reader
	// MD5 and Serial are what the request declared: the hex MD5 of Body,
	// and the serial Body's state holds.
	MD5    string
	Serial int64
	By     string
}

// CurrentState returns workspace name's current state, ErrNoState when it
// has none.
func (s *Service) CurrentState(ctx context.Context, c repo.Config, name string) (State, error) {
	if _, err := s.Workspace(ctx, c, name); err != nil {
		return State{}, err
	}
	latest, err := s.fileAt(ctx, c, tfbe.LatestPath(name))
	if err != nil {
		return State{}, err
	}
	if latest == nil {
		return State{}, fmt.Errorf("%w: %s has no current state", ErrNoState, name)
	}
	return s.stateOf(ctx, c, name, *latest)
}

// State returns state version ms of workspace name.
func (s *Service) State(ctx context.Context, c repo.Config, name string, ms int64) (State, error) {
	if _, err := s.Workspace(ctx, c, name); err != nil {
		return State{}, err
	}
	// The current state is the version when both name it, as the index
	// has it.
	latest, err := s.fileAt(ctx, c, tfbe.LatestPath(name))
	if err != nil {
		return State{}, err
	}
	if latest != nil && latest.Created.UnixMilli() == ms {
		return s.stateOf(ctx, c, name, *latest)
	}
	earlier, err := s.fileAt(ctx, c, tfbe.StatePath(name, ms))
	if err != nil {
		return State{}, err
	}
	if earlier != nil {
		return s.stateOf(ctx, c, name, *earlier)
	}
	return State{}, fmt.Errorf("%w: %s", ErrNoState, tfbe.StateID(c.Key, name, ms))
}

// States lists workspace name's state versions, newest first.
func (s *Service) States(ctx context.Context, c repo.Config, name string) ([]State, error) {
	if _, err := s.Workspace(ctx, c, name); err != nil {
		return nil, err
	}
	files, err := s.stateFiles(ctx, c, name)
	if err != nil {
		return nil, err
	}
	out := make([]State, 0, len(files))
	for _, it := range files {
		st, err := s.stateOf(ctx, c, name, it)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	slices.SortStableFunc(out, func(a, b State) int {
		return cmp.Or(cmp.Compare(b.Ms, a.Ms), boolOrder(b.Current, a.Current))
	})
	return out, nil
}

func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// ReadState returns the plain content of state version ms of workspace
// name, unsealed when it is sealed, with the version.
func (s *Service) ReadState(
	ctx context.Context,
	c repo.Config,
	name string,
	ms int64,
) ([]byte, State, error) {
	st, err := s.State(ctx, c, name, ms)
	if err != nil {
		return nil, State{}, err
	}
	stored, err := tfbe.ReadStored(ctx, s.Items.Blobs, st.Meta.Stored)
	if err != nil {
		return nil, State{}, err //nolint:wrapcheck // the format names it
	}
	plain, err := tfbe.Open(s.Keys, c.Key, name, stored)
	if err != nil {
		return nil, State{}, err //nolint:wrapcheck // the format names it
	}
	return plain, st, nil
}

// NewState names the next state version of workspace name, for a create
// that returns an upload URL: WriteState then stores it under that name.
// Nothing is stored, so the name is the next one only until another write.
// Another user's lock refuses it with a *LockedError.
func (s *Service) NewState(ctx context.Context, c repo.Config, name, by string) (int64, error) {
	var ms int64
	err := s.store().Tx(ctx, func(ctx context.Context) error {
		files, err := s.writable(ctx, c, name, by)
		if err != nil {
			return err
		}
		ms = next(s.now(), files)
		return nil
	})
	if err != nil {
		return 0, err //nolint:wrapcheck // the transaction returns ours
	}
	return ms, nil
}

// WriteState stores w's state as workspace name's current state (decision
// 0041, "Storage"), in one transaction: the current state, if any, moves
// to its timestamped name, the new one becomes state.latest.json, and the
// format's hooks index both. The state is sealed under the master key
// while c's encryptStates is on (or unset, which means on); otherwise it
// is stored as it is.
//
// The state must be a Terraform state whose MD5 and serial are the
// declared ones, or an *InvalidStateError. Another user's lock refuses it
// with a *LockedError; the holder and anyone writing to an unlocked
// workspace may write. A version w.Ms names that is stored already is
// ErrWritten. Serial and lineage are not compared with the current
// state's: Terraform does that, and -force skips it.
func (s *Service) WriteState(
	ctx context.Context,
	c repo.Config,
	name string,
	w StateWrite,
) (State, error) {
	// A sealed state is one AEAD message, so the state is read whole; a
	// plain one is too, so it is checked before anything is stored.
	plain, err := io.ReadAll(w.Body)
	if err != nil {
		return State{}, fmt.Errorf("terraformstate: read the state: %w", err)
	}
	stored := plain
	if c.EncryptStates == nil || *c.EncryptStates {
		if stored, err = tfbe.Seal(s.Keys, c.Key, name, plain); err != nil {
			return State{}, err //nolint:wrapcheck // the format names it
		}
	}
	meta, err := tfbe.PlainMeta(plain, stored)
	switch {
	case err != nil:
		return State{}, &InvalidStateError{
			Message: "The state is not a Terraform state: " + err.Error(),
		}
	case !strings.EqualFold(meta.MD5, w.MD5):
		return State{}, &InvalidStateError{Message: fmt.Sprintf(
			"The state's MD5 is %s, not the md5 %q the request declared", meta.MD5, w.MD5)}
	case meta.Serial != w.Serial:
		return State{}, &InvalidStateError{Message: fmt.Sprintf(
			"The state's serial is %d, not the serial %d the request declared",
			meta.Serial,
			w.Serial,
		)}
	}
	info, err := s.Items.Upload(ctx, item.Deploy{
		Target: target(c, tfbe.LatestPath(name)), Body: bytes.NewReader(stored),
	})
	if err != nil {
		return State{}, fmt.Errorf("terraformstate: store the state of %s: %w", name, err)
	}
	ms := w.Ms
	err = s.store().Tx(ctx, func(ctx context.Context) error {
		files, err := s.writable(ctx, c, name, w.By)
		if err != nil {
			return err
		}
		if ms == 0 {
			ms = next(s.now(), files)
		} else if slices.ContainsFunc(files, func(it types.Item) bool {
			f, _ := tfbe.FileOf(it)
			return f.Ms == ms
		}) {
			return fmt.Errorf("%w: %s", ErrWritten, tfbe.StateID(c.Key, name, ms))
		}
		ctx = tfbe.Writing(ctx, &meta)
		items := s.at(tfbe.Created(ms))
		for _, it := range files {
			if f, _ := tfbe.FileOf(it); f.Latest {
				if err := s.retire(ctx, items, c, name, f.Ms, w.By); err != nil {
					return err
				}
			}
		}
		if _, err := items.DeployCommitted(ctx, item.Deploy{
			Target: target(c, tfbe.LatestPath(name)), By: w.By, Trusted: true, IfAbsent: true,
		}, info); err != nil {
			return fmt.Errorf("terraformstate: store the state of %s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return State{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return s.State(ctx, c, name, ms)
}

// retire moves workspace name's current state, version ms, to its
// timestamped name. The server moves it, so a writer who may deploy needs
// no right to delete for it.
func (s *Service) retire(
	ctx context.Context,
	items *item.Service,
	c repo.Config,
	name string,
	ms int64,
	by string,
) error {
	problem, err := items.Env().Move(ctx, c, tfbe.LatestPath(name), tfbe.StatePath(name, ms), by)
	if err != nil {
		return fmt.Errorf("terraformstate: keep the state of %s: %w", name, err)
	}
	if problem != "" {
		return fmt.Errorf("terraformstate: keep the state of %s: %s: %w", name, problem,
			ErrWritten)
	}
	return nil
}

// writable holds workspace name for a state write by by, until ctx's
// transaction ends, and returns its state files. Another user's lock
// refuses the write (decision 0041, "The lock rule").
func (s *Service) writable(
	ctx context.Context,
	c repo.Config,
	name, by string,
) ([]types.Item, error) {
	folder, err := s.holdWorkspace(ctx, c, name)
	if err != nil {
		return nil, err
	}
	lock, err := s.lockOf(ctx, folder)
	if err != nil {
		return nil, err
	}
	if lock != nil && lock.By != by {
		return nil, &LockedError{Workspace: name, Holder: lock.By}
	}
	return s.stateFiles(ctx, c, name)
}

// next is the name of a new state version: now's milliseconds, or one more
// than the newest of files when that is later, so names stay unique
// within the workspace (decision 0041, "Identifiers").
func next(now time.Time, files []types.Item) int64 {
	ms := now.UnixMilli()
	for _, it := range files {
		if f, _ := tfbe.FileOf(it); f.Ms >= ms {
			ms = f.Ms + 1
		}
	}
	return ms
}

// stateFiles lists workspace name's state files.
func (s *Service) stateFiles(
	ctx context.Context,
	c repo.Config,
	name string,
) ([]types.Item, error) {
	var out []types.Item
	for page := (store.Page{Limit: store.MaxLimit}); ; {
		children, next, err := s.store().Items().
			Children(ctx, c.Key, false, tfbe.Folder(name), page)
		if err != nil {
			return nil, fmt.Errorf("terraformstate: states of %s: %w", name, err)
		}
		for _, it := range children {
			if _, ok := tfbe.FileOf(it); ok {
				out = append(out, it)
			}
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

// stateOf is the state version it, a state file of workspace name: as the
// package index describes it while the index read the content it holds,
// and otherwise as the content says.
func (s *Service) stateOf(
	ctx context.Context,
	c repo.Config,
	name string,
	it types.Item,
) (State, error) {
	f, _ := tfbe.FileOf(it)
	st := State{
		Repo: c.Key, Workspace: name, Ms: f.Ms, Current: f.Latest, Path: it.FullPath(),
		CreatedBy: it.CreatedBy,
	}
	pv, ok, err := pkgindex.VersionOf(ctx, s.store(), it.ID)
	if err != nil {
		return State{}, err //nolint:wrapcheck // pkgindex wraps its own
	}
	if m, cached := tfbe.DecodeMeta(pv.Metadata); ok && cached && m.Stored == it.Actual.SHA256 {
		st.Meta = m
		return st, nil
	}
	stored, err := tfbe.ReadStored(ctx, s.Items.Blobs, it.Actual.SHA256)
	if err != nil {
		return State{}, err //nolint:wrapcheck // the format names it
	}
	st.Meta = tfbe.MetaOf(s.Keys, c.Key, name, stored)
	return st, nil
}
