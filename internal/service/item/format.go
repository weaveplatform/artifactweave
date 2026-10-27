package item

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
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

// Env is the environment of one operation a format offers beside its hooks,
// such as Maven's calculateMetadata, which reads and queues as a hook does.
func (s *Service) Env() format.Env { return s.env() }

// env is the environment of one operation's hooks.
func (s *Service) env() format.Env {
	env := format.Env{
		Store:    s.Store,
		Now:      s.now,
		Layouts:  &layouts{s: s, seen: map[string]*layout.Layout{}},
		Derive:   s.derive,
		Underive: s.underive,
		Remove:   s.removeFiles,
		Enqueue:  s.enqueue,
		Move:     s.moveFile,
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
	if pending, ok := ctx.Value(transferWorkKey{}).(transferWork); ok {
		pending[queuedWorkKey{kind: kind, key: key}] = append([]byte(nil), payload...)
		return nil
	}
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
		Properties: d.Properties, By: d.By, Trusted: true,
	}
	var out types.Item
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.writeFile(ctx, dep, fileWrite{
			actual: digestsOf(info), size: info.Size, shouldReplace: len(d.Properties) > 0,
		}, s.now())
		return err
	})
	if err != nil {
		return types.Item{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return out, nil
}

// underive removes a file a format derived, in ctx's transaction or one of
// its own, with no hook and no trash, and releases its content.
func (s *Service) underive(ctx context.Context, c repo.Config, cached bool, p string) error {
	full := strings.Trim(p, "/")
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		it, err := s.Store.Items().Get(ctx, c.Key, cached, full)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("item: get %s: %w", full, err)
		case it.Folder:
			// A folder would take everything below it.
			return fmt.Errorf("%w: %s is a folder", ErrConflict, full)
		}
		res, err := s.Store.Items().Delete(ctx, c.Key, cached, full)
		if err != nil {
			return fmt.Errorf("item: delete %s: %w", full, err)
		}
		for sha, n := range res.BlobRefs {
			if _, err := s.Store.Blobs().Unref(ctx, sha, n); err != nil {
				return fmt.Errorf("item: release blob %s: %w", sha, err)
			}
		}
		return nil
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
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

// guardWrite runs only inside the transaction that will land the write.
func guardWrite(
	ctx context.Context,
	h format.Handler,
	env format.Env,
	c repo.Config,
	w format.Write,
) error {
	if g, ok := h.(format.WriteGuard); ok {
		return g.GuardWrite(ctx, env, c, w) //nolint:wrapcheck // the format's own error
	}
	return nil
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
// transaction, or one of its own, once the format admits the change
// (format.AnnotationAdmitter), and with the format's Annotated hook, so a
// format whose index follows properties stays in step whichever API made
// the change.
func (s *Service) Annotate(
	ctx context.Context,
	c repo.Config,
	it types.Item,
	set types.Properties,
	remove []string,
) error {
	return s.annotate(ctx, c, it, set, remove, true)
}

// annotate is Annotate, asking the format to admit the change first when
// admit is set. A restore brings back properties that were set already,
// so it does not ask.
func (s *Service) annotate(
	ctx context.Context,
	c repo.Config,
	it types.Item,
	set types.Properties,
	remove []string,
	admit bool,
) error {
	now := s.now()
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		h := s.format(c.PackageType)
		if a, ok := h.(format.AnnotationAdmitter); ok && admit {
			if err := a.AdmitAnnotation(ctx, s.env(), c, it,
				format.Annotation{Set: set, Removed: remove, At: now}); err != nil {
				return err //nolint:wrapcheck // the format's own error
			}
		}
		if err := s.Store.Items().PatchProperties(ctx, it.ID, set, remove, now); err != nil {
			return fmt.Errorf("item: properties of %s: %w", it.FullPath(), err)
		}
		if _, ok := h.(format.Annotator); !ok {
			return nil
		}
		return s.annotated(ctx, h, s.env(), c, it,
			format.Annotation{Set: set, Removed: remove, At: now})
	})
	if err != nil {
		return err //nolint:wrapcheck // the transaction returns ours
	}
	return nil
}

// Resolve returns the path a download d from c, a repository that is not
// virtual, reads: the format's resolution of it, or d.Path itself. The
// derived work the format owes that path is done first (EnsureFresh).
func (s *Service) Resolve(ctx context.Context, c repo.Config, d format.Download) (string, error) {
	h := s.format(c.PackageType)
	out := d.Path
	if r, ok := h.(format.Resolver); ok {
		var err error
		out, err = r.Resolve(ctx, s.env(), c, d)
		if err != nil {
			return "", err //nolint:wrapcheck // the format's own error
		}
		out = strings.Trim(out, "/")
	}
	if err := s.fresh(ctx, h, c, d.Cached, out); err != nil {
		return "", err
	}
	return out, nil
}

// Reindex rebuilds c's package index from its item tree through its
// format; ErrNoReindex when the format keeps none.
func (s *Service) Reindex(ctx context.Context, c repo.Config) error {
	return s.reindex(ctx, c, nil)
}

func (s *Service) reindex(ctx context.Context, c repo.Config, request *format.WorkRequest) error {
	r, ok := s.format(c.PackageType).(format.Reindexer)
	if !ok {
		return ErrNoReindex
	}
	env := s.env()
	if request != nil {
		env.SkipProperties = request.SkipProperties
	}
	if request != nil && request.Source != nil {
		env.ReindexSource = request.Source
		env.CheckReindex = func(ctx context.Context) error {
			if s.ReindexAuthorize == nil {
				return fmt.Errorf("%w: remote reindex authorizer unavailable", work.ErrPermanent)
			}
			return s.ReindexAuthorize(ctx, c, *request)
		}
		if err := env.CheckReindex(ctx); err != nil {
			return fmt.Errorf("item: current reindex authority: %w", err)
		}
	}
	return r.Reindex(ctx, env, c) //nolint:wrapcheck // the format's own error
}

// ReindexPath rebuilds the part of c's package index that the files under
// path give, through a format that is a PathReindexer, or the whole index
// through any other Reindexer; ErrNoReindex when the format keeps none.
func (s *Service) ReindexPath(ctx context.Context, c repo.Config, path string) error {
	if r, ok := s.format(c.PackageType).(format.PathReindexer); ok {
		return r.ReindexPath(ctx, s.env(), c, path) //nolint:wrapcheck // the format's own error
	}
	return s.Reindex(ctx, c)
}
