package item

import (
	"context"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Sidecars returns the paths in c of the sidecars that the file at p owns
// and that c's format keeps with it (format.SidecarKeeper), whether or not
// any exists; none when the format keeps none.
func (s *Service) Sidecars(c repo.Config, p string) []string {
	if k, ok := s.format(c.PackageType).(format.SidecarKeeper); ok {
		return k.Sidecars(c, p)
	}
	return nil
}

// sidecarsOf returns the stored sidecars of the files among items, in c's
// own content or its cache, that items does not hold already. A folder at
// a sidecar's path is no sidecar.
func (s *Service) sidecarsOf(
	ctx context.Context,
	c repo.Config,
	cached bool,
	items []types.Item,
) ([]types.Item, error) {
	k, ok := s.format(c.PackageType).(format.SidecarKeeper)
	if !ok {
		return nil, nil
	}
	have := make(map[string]bool, len(items))
	for _, it := range items {
		have[it.FullPath()] = true
	}
	var out []types.Item
	for _, it := range items {
		if it.Folder {
			continue
		}
		for _, p := range k.Sidecars(c, it.FullPath()) {
			if have[p] {
				continue
			}
			have[p] = true
			sc, ok, err := s.sidecar(ctx, c, cached, p)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, sc)
			}
		}
	}
	return out, nil
}

// sidecar returns the file at p, and false when there is none.
func (s *Service) sidecar(
	ctx context.Context,
	c repo.Config,
	cached bool,
	p string,
) (types.Item, bool, error) {
	it, err := s.Store.Items().Get(ctx, c.Key, cached, p)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return it, false, nil
	case err != nil:
		return it, false, fmt.Errorf("item: get %s: %w", p, err)
	}
	return it, !it.Folder, nil
}

// deleteSidecars deletes the sidecars a delete took with the files it
// named, adding what each released to res.
func (s *Service) deleteSidecars(
	ctx context.Context,
	t Target,
	sidecars []types.Item,
	res store.DeleteResult,
) (store.DeleteResult, error) {
	for _, sc := range sidecars {
		more, err := s.Store.Items().Delete(ctx, t.Repo.Key, t.Cached, sc.FullPath())
		if err != nil {
			return res, fmt.Errorf("item: delete %s: %w", sc.FullPath(), err)
		}
		res.Files += more.Files
		res.Folders += more.Folders
		res.Bytes += more.Bytes
		if len(more.BlobRefs) > 0 && res.BlobRefs == nil {
			res.BlobRefs = map[string]int64{}
		}
		for sha, n := range more.BlobRefs {
			res.BlobRefs[sha] += n
		}
	}
	return res, nil
}

// planSidecars plans, after each planned file of a copy or move, the
// sidecars its source format keeps with it, each landing at its sidecar
// path beside the file's target and admitted and checked as the file was. A
// sidecar the transfer names in its own right, as a folder's does, is
// planned with the other sources instead, so it is carried once. A file
// whose sidecar cannot land does not go either: a chart moved without its
// provenance file, or copied beside another chart's, fails helm's
// verification (decision 0039). Both are reported.
func (s *Service) planSidecars(
	ctx context.Context,
	t Transfer,
	res *TransferResult,
	sources []types.Item,
	steps []step,
) ([]step, error) {
	k, ok := s.format(t.From.Repo.PackageType).(format.SidecarKeeper)
	if !ok {
		return steps, nil
	}
	named := make(map[string]bool, len(sources))
	for _, src := range sources {
		named[src.FullPath()] = true
	}
	hooks, env := s.format(t.To.Repo.PackageType), s.env()
	out := make([]step, 0, len(steps))
	for _, st := range steps {
		out = append(out, st)
		if st.src.Folder {
			continue
		}
		followers, problem, err := s.planFollowers(ctx, k, hooks, env, t, st, named)
		if err != nil {
			return nil, err
		}
		if problem == "" {
			out = append(out, followers...)
			continue
		}
		out = out[:len(out)-1]
		res.Failed += 2
		res.Messages = append(res.Messages, Message{LevelError, problem}, Message{
			LevelError, fmt.Sprintf("Cannot transfer %s:%s to %s:%s: its sidecar cannot go with it",
				t.From.Repo.Key, st.src.FullPath(), t.To.Repo.Key, st.dst),
		})
		if t.FailFast {
			break
		}
	}
	return out, nil
}

// planFollowers plans the stored sidecars of the file st transfers, or
// says why one cannot land. A target the format would give no such sidecar,
// as when a copy renames a chart to something that is none, takes none.
func (s *Service) planFollowers(
	ctx context.Context,
	k format.SidecarKeeper,
	hooks format.Handler,
	env format.Env,
	t Transfer,
	st step,
	named map[string]bool,
) ([]step, string, error) {
	owned := k.Sidecars(t.From.Repo, st.src.FullPath())
	beside := k.Sidecars(t.From.Repo, st.dst)
	var out []step
	for i, p := range owned {
		if named[p] || i >= len(beside) {
			continue
		}
		sc, ok, err := s.sidecar(ctx, t.From.Repo, t.From.Cached, p)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		planned, problem, err := s.planStep(ctx, hooks, env, t, sc, beside[i])
		if err != nil || problem != "" {
			return nil, problem, err
		}
		out = append(out, planned)
	}
	return out, "", nil
}
