package item

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Freshener brings derived work up to date before a reader reads what it
// derives; *work.Queue is one.
type Freshener interface {
	EnsureFresh(ctx context.Context, kind, key string) error
}

// ErrDuplicateKind reports two formats that claim one kind of work.
var ErrDuplicateKind = errors.New("item: two formats claim one kind of work")

// RegisterWork registers with q a handler for the reindex kinds every
// format shares and for every kind of derived work a format runs, each run
// through s, on a timer for the kinds s.Periodic names. Call it before
// q.Run, which leases only the kinds registered when it starts.
func (s *Service) RegisterWork(q *work.Queue) error {
	owners := map[string]reflect.Type{}
	for _, kind := range []string{format.KindReindex, format.KindReindexPath} {
		// The service owns them, so a format that claims one is refused
		// as a second owner.
		owners[kind] = reflect.TypeOf(s)
		if err := q.Handle(kind, s.reindexWork); err != nil {
			return fmt.Errorf("item: register %s: %w", kind, err)
		}
	}
	for _, t := range repo.AllowedPackageTypes(repo.Local) {
		h := s.format(t)
		w, ok := h.(format.Worker)
		if !ok {
			continue
		}
		for _, kind := range w.WorkKinds() {
			if owner, seen := owners[kind]; seen {
				// One format serves several types, as Maven's serves four.
				// Types, not values: a handler need not be comparable.
				if owner != reflect.TypeOf(h) {
					return fmt.Errorf("%w: %q", ErrDuplicateKind, kind)
				}
				continue
			}
			owners[kind] = reflect.TypeOf(h)
			run := func(ctx context.Context, it types.WorkItem) error {
				return w.Work(ctx, s.env(), it) //nolint:wrapcheck // the format's own error
			}
			var err error
			if every, ok := s.Periodic[kind]; ok {
				err = q.HandleEvery(kind, every, run)
			} else {
				err = q.Handle(kind, run)
			}
			if err != nil {
				return fmt.Errorf("item: register %s: %w", kind, err)
			}
		}
	}
	return nil
}

// reindexWork runs a queued reindex, of a whole repository or of a path in
// one, through the format of the repository's package type. A repository
// that is gone, or whose format keeps no index, owes nothing: no run could
// do the work, so failing would only retry it until the queue parked it.
func (s *Service) reindexWork(ctx context.Context, it types.WorkItem) error {
	key, path := it.Key, ""
	if it.Kind == format.KindReindexPath {
		key, path, _ = strings.Cut(it.Key, "/")
	}
	c, err := s.Store.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("item: reindex %s: %w", key, err)
	case it.Kind == format.KindReindexPath:
		err = s.ReindexPath(ctx, c, path)
	default:
		err = s.Reindex(ctx, c)
	}
	if errors.Is(err, ErrNoReindex) {
		return nil
	}
	return err
}

// fresh does the derived work h owes a download of p from c before it is
// read. Without a queue that runs work here, nothing is run: the work waits
// for the workers.
func (s *Service) fresh(
	ctx context.Context,
	h format.Handler,
	c repo.Config,
	cached bool,
	p string,
) error {
	w, ok := h.(format.Worker)
	if !ok {
		return nil
	}
	f, ok := s.Work.(Freshener)
	if !ok {
		return nil
	}
	for _, ref := range w.Owed(c, cached, p) {
		err := f.EnsureFresh(ctx, ref.Kind, ref.Key)
		switch {
		case errors.Is(err, work.ErrNoHandler):
			// A queue built without the formats' handlers, as a test's may
			// be, runs none of their work: the read is served as stored.
			continue
		case err != nil:
			return fmt.Errorf("item: %s work for %s: %w", ref.Kind, p, err)
		}
	}
	return nil
}
