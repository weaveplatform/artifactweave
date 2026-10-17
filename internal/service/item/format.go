package item

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrNoReindex reports a package type whose format keeps no index to
// rebuild.
var ErrNoReindex = errors.New("item: the format keeps no index to rebuild")

// Enqueuer asks the persistent work queue for work; *work.Queue is one.
type Enqueuer interface {
	Enqueue(ctx context.Context, kind, key string, payload []byte) error
}

// format returns the format of a package type.
func (s *Service) format(t repo.PackageType) format.Handler {
	if s.Formats != nil {
		return s.Formats(t)
	}
	return formats.For(t)
}

// env is the environment of one operation's hooks.
func (s *Service) env() format.Env {
	env := format.Env{
		Store:   s.Store,
		Now:     s.now,
		Layouts: &layouts{s: s, seen: map[string]*layout.Layout{}},
		Derive:  s.derive,
		Enqueue: s.enqueue,
	}
	// A nil blob.Store must not become a non-nil format.Blobs.
	if s.Blobs != nil {
		env.Blobs = s.Blobs
	}
	return env
}

// layouts compiles each layout an operation asks for once. It lives no
// longer than the operation, so a layout the descriptor changes is read
// afresh by the next one.
type layouts struct {
	s    *Service
	seen map[string]*layout.Layout
}

func (l *layouts) Layout(ctx context.Context, name string) (*layout.Layout, error) {
	if c, ok := l.seen[name]; ok {
		return c, nil
	}
	c, err := l.s.Layout(ctx, name)
	if err != nil {
		return nil, err
	}
	l.seen[name] = c
	return c, nil
}

// enqueue asks for work inside ctx's transaction. Without a queue the item
// goes straight into the store, where the workers find it on their next
// poll; the queue also wakes this node's idle workers once the write
// commits.
func (s *Service) enqueue(ctx context.Context, kind, key string, payload []byte) error {
	if s.Work != nil {
		return s.Work.Enqueue(ctx, kind, key, payload) //nolint:wrapcheck // the queue names itself
	}
	if err := s.Store.WorkQueue().Enqueue(ctx, kind, key, payload, s.now()); err != nil {
		return fmt.Errorf("item: enqueue %s/%s: %w", kind, key, err)
	}
	return nil
}

// derive writes a format's derived item: its content into the blob store,
// then the item in ctx's transaction (or one of its own), with no hook.
func (s *Service) derive(ctx context.Context, d format.Derived) (types.Item, error) {
	u, err := s.Blobs.BeginUpload(ctx)
	if err != nil {
		return types.Item{}, fmt.Errorf("item: begin upload: %w", err)
	}
	if _, err := u.Append(ctx, d.Body); err != nil {
		_ = u.Abort(context.WithoutCancel(ctx))
		return types.Item{}, fmt.Errorf("item: upload: %w", err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		_ = u.Abort(context.WithoutCancel(ctx))
		return types.Item{}, fmt.Errorf("item: commit upload: %w", err)
	}
	dep := Deploy{
		Target:     Target{Repo: d.Repo, Cached: d.Cached, Path: d.Path},
		Properties: d.Properties, By: d.By,
	}
	var out types.Item
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.writeFile(ctx, dep, digestsOf(info), info.Size, types.Checksums{}, s.now())
		return err
	})
	if err != nil {
		return types.Item{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return out, nil
}

// inspect runs the format's Inspect on a write whose content is stored,
// outside any transaction, and returns env carrying what it read.
func inspect(
	ctx context.Context,
	h format.Handler,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Env, error) {
	in, ok := h.(format.Inspector)
	if !ok {
		return env, nil
	}
	var err error
	env.Inspection, err = in.Inspect(ctx, env, c, w)
	return env, err //nolint:wrapcheck // the format's own error
}

// admit runs the format's Admit, and checks that a rewritten path still
// names an item.
func admit(
	ctx context.Context,
	h format.Handler,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	a, ok := h.(format.Admitter)
	if !ok {
		return w, nil
	}
	out, err := a.Admit(ctx, env, c, w)
	if err != nil {
		return w, err //nolint:wrapcheck // the format's own error
	}
	out.Path = strings.Trim(out.Path, "/")
	if out.Path == "" {
		return w, fmt.Errorf("%w: the format admitted %s to the repository root",
			types.ErrInvalidItemPath, w.Path)
	}
	return out, nil
}

// annotated runs the format's Annotated hook for a change to the
// properties of it, inside ctx's transaction. An empty change runs nothing.
func (s *Service) annotated(
	ctx context.Context,
	h format.Handler,
	env format.Env,
	c repo.Config,
	it types.Item,
	a format.Annotation,
) error {
	an, ok := h.(format.Annotator)
	if !ok || len(a.Set) == 0 && len(a.Removed) == 0 {
		return nil
	}
	return an.Annotated(ctx, env, c, it, a) //nolint:wrapcheck // the format's own error
}

// Annotate changes the properties of it, an item of c: set replaces the
// values of its keys and remove drops keys. It runs inside ctx's
// transaction, or one of its own, with the format's Annotated hook, so a
// format whose index follows properties stays in step whichever API made
// the change.
func (s *Service) Annotate(
	ctx context.Context,
	c repo.Config,
	it types.Item,
	set types.Properties,
	remove []string,
) error {
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Store.Items().PatchProperties(ctx, it.ID, set, remove); err != nil {
			return fmt.Errorf("item: properties of %s: %w", it.FullPath(), err)
		}
		h := s.format(c.PackageType)
		if _, ok := h.(format.Annotator); !ok {
			return nil
		}
		return s.annotated(ctx, h, s.env(), c, it, format.Annotation{Set: set, Removed: remove})
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// Resolve returns the path a download d from c, a repository that is not
// virtual, reads: the format's resolution of it, or d.Path itself.
func (s *Service) Resolve(ctx context.Context, c repo.Config, d format.Download) (string, error) {
	r, ok := s.format(c.PackageType).(format.Resolver)
	if !ok {
		return d.Path, nil
	}
	out, err := r.Resolve(ctx, s.env(), c, d)
	if err != nil {
		return "", err //nolint:wrapcheck // the format's own error
	}
	return strings.Trim(out, "/"), nil
}

// Reindex rebuilds c's package index from its item tree through its
// format; ErrNoReindex when the format keeps none.
func (s *Service) Reindex(ctx context.Context, c repo.Config) error {
	r, ok := s.format(c.PackageType).(format.Reindexer)
	if !ok {
		return ErrNoReindex
	}
	return r.Reindex(ctx, s.env(), c) //nolint:wrapcheck // the format's own error
}
