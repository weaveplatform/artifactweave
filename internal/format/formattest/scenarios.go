package formattest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// scenario is one part of the contract, run on a fresh world.
type scenario struct {
	name string
	run  func(w *world) error
}

var scenarios = []scenario{
	{"deploy", deployScenario},
	{"overwrite", overwriteScenario},
	{"delete", deleteScenario},
	{"copy", copyScenario},
	{"move", moveScenario},
	{"restore", restoreScenario},
	{"properties", propertiesScenario},
	{"rollback", rollbackScenario},
	{"work", workScenario},
	{"patterns", patternsScenario},
	{"cache", cacheScenario},
}

// three deploys alpha 1.0, alpha 2.0 and beta 1.0 to c and returns them.
func (w *world) three(c repo.Config) ([]Version, error) {
	vs := []Version{
		w.version("alpha", "1.0", 0), w.version("alpha", "2.0", 0), w.version("beta", "1.0", 0),
	}
	for _, v := range vs {
		if err := w.deploy(c, v, nil); err != nil {
			return nil, err
		}
	}
	return vs, nil
}

// Deployed versions are indexed, each linking its files.
func deployScenario(w *world) error {
	vs, err := w.three(w.a)
	if err != nil {
		return err
	}
	return errors.Join(w.expect(w.a, vs...), w.verify(w.a))
}

// Redeploying a version over itself keeps it, as the overwritten content
// now describes it.
func overwriteScenario(w *world) error {
	if err := w.deploy(w.a, w.version("alpha", "1.0", 0), nil); err != nil {
		return err
	}
	again := w.version("alpha", "1.0", 1)
	if err := w.deploy(w.a, again, nil); err != nil {
		return err
	}
	return errors.Join(w.expect(w.a, again), w.verify(w.a))
}

// Deleting a version's files one by one leaves the index right after each,
// and the version gone after the last; deleting the repository's content
// leaves no index.
func deleteScenario(w *world) error {
	vs, err := w.three(w.a)
	if err != nil {
		return err
	}
	for _, f := range vs[1].Files {
		w.tick()
		if _, err := w.items.Delete(
			w.ctx,
			item.Target{Repo: w.a, Path: f.Path},
			"formattest",
		); err != nil {
			return fmt.Errorf("delete %s: %w", f.Path, err)
		}
		if err := w.verify(w.a); err != nil {
			return fmt.Errorf("after deleting %s: %w", f.Path, err)
		}
	}
	if err := w.expect(w.a, vs[0], vs[2]); err != nil {
		return err
	}
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.a}, "formattest"); err != nil {
		return fmt.Errorf("delete the repository's content: %w", err)
	}
	return errors.Join(w.expect(w.a), w.verify(w.a))
}

// A copy indexes its target as the source is indexed, a whole repository
// at once or one file at a time, and leaves the source as it was.
func copyScenario(w *world) error {
	vs, err := w.three(w.a)
	if err != nil {
		return err
	}
	if err := w.transfer(w.a, "", w.b, "", false); err != nil {
		return err
	}
	if err := errors.Join(
		w.expect(w.a, vs...),
		w.expect(w.b, vs...),
		w.verify(w.a, w.b),
	); err != nil {
		return err
	}
	for _, f := range vs[0].Files {
		if err := w.transfer(w.a, f.Path, w.c, f.Path, false); err != nil {
			return err
		}
		if err := w.verify(w.c); err != nil {
			return fmt.Errorf("after copying %s: %w", f.Path, err)
		}
	}
	return w.expect(w.c, vs[0])
}

// A move indexes its target and unindexes its source.
func moveScenario(w *world) error {
	vs, err := w.three(w.a)
	if err != nil {
		return err
	}
	for _, f := range vs[0].Files {
		if err := w.transfer(w.a, f.Path, w.c, f.Path, true); err != nil {
			return err
		}
		if err := w.verify(w.a, w.c); err != nil {
			return fmt.Errorf("after moving %s: %w", f.Path, err)
		}
	}
	if err := w.transfer(w.a, "", w.b, "", true); err != nil {
		return err
	}
	return errors.Join(
		w.expect(w.a), w.expect(w.b, vs[1:]...), w.expect(w.c, vs[0]), w.verify(w.a, w.b, w.c),
	)
}

// transfer copies or moves from:src to to:dst, every item of it.
func (w *world) transfer(
	from repo.Config,
	src string,
	to repo.Config,
	dst string,
	move bool,
) error {
	w.tick()
	res, err := w.items.Transfer(w.ctx, item.Transfer{
		From: item.Target{Repo: from, Path: src}, To: item.Target{Repo: to, Path: dst},
		Move: move, By: "formattest",
	})
	if err == nil && res.Failed > 0 {
		err = fmt.Errorf("%w: %d items failed: %v", errBroken, res.Failed, res.Messages)
	}
	if err != nil {
		return fmt.Errorf(
			"transfer %s:%s to %s:%s (move %v): %w",
			from.Key,
			src,
			to.Key,
			dst,
			move,
			err,
		)
	}
	return nil
}

// Restoring from the trash can indexes a version again, file by file or a
// whole repository's content at once.
func restoreScenario(w *world) error {
	vs, err := w.three(w.a)
	if err != nil {
		return err
	}
	if _, err := w.items.Delete(w.ctx, item.Target{Repo: w.a}, "formattest"); err != nil {
		return fmt.Errorf("delete the repository's content: %w", err)
	}
	if err := w.expect(w.a); err != nil {
		return err
	}
	for _, f := range vs[0].Files {
		if err := w.restore(f.Path); err != nil {
			return err
		}
		if err := w.verify(w.a); err != nil {
			return fmt.Errorf("after restoring %s: %w", f.Path, err)
		}
	}
	if err := w.restore(""); err != nil {
		return err
	}
	return errors.Join(w.expect(w.a, vs...), w.verify(w.a))
}

// restore restores what was trashed from repository a at p.
func (w *world) restore(p string) error {
	w.tick()
	if _, err := w.trash.Restore(
		w.ctx,
		keyA,
		p,
		trash.Destination{Repo: w.a},
		"formattest",
		nil,
	); err != nil {
		return fmt.Errorf("restore %s: %w", p, err)
	}
	return nil
}

// Every way of changing properties the item service offers keeps the
// index right: deploying with them (matrix parameters), setting, changing
// and removing them (the properties API and AQL properties.update call
// Annotate), and a copy or move carrying them over.
func propertiesScenario(w *world) error {
	props := w.f.Properties
	v := w.version("alpha", "1.0", 0)
	if err := w.deploy(w.a, v, props[0]); err != nil {
		return err
	}
	if err := w.verify(w.a); err != nil {
		return fmt.Errorf("after deploying with %v: %w", props[0], err)
	}
	keys := map[string]bool{}
	for _, set := range props {
		for k := range set {
			keys[k] = true
		}
	}
	steps := make([]func() (string, error), 0, len(props)+1)
	for _, set := range props[1:] {
		steps = append(steps, func() (string, error) {
			return fmt.Sprintf("setting %v", set), w.annotate(w.a, v, set, nil)
		})
	}
	removed := slices.Sorted(maps.Keys(keys))
	steps = append(steps, func() (string, error) {
		return fmt.Sprintf("removing %v", removed), w.annotate(w.a, v, nil, removed)
	})
	for _, step := range steps {
		what, err := step()
		if err != nil {
			return err
		}
		if err := w.verify(w.a); err != nil {
			return fmt.Errorf("after %s: %w", what, err)
		}
	}
	if err := w.annotate(w.a, v, props[len(props)-1], nil); err != nil {
		return err
	}
	if err := w.transfer(w.a, "", w.b, "", false); err != nil {
		return err
	}
	if err := w.transfer(w.b, "", w.c, "", true); err != nil {
		return err
	}
	return errors.Join(w.expect(w.a, v), w.expect(w.c, v), w.verify(w.a, w.b, w.c))
}

// annotate changes the properties of every file of v in c.
func (w *world) annotate(c repo.Config, v Version, set types.Properties, remove []string) error {
	for _, f := range v.Files {
		it, err := w.file(c, f.Path)
		if err != nil {
			return err
		}
		w.tick()
		if err := w.items.Annotate(w.ctx, c, it, set, remove); err != nil {
			return fmt.Errorf("annotate %s: %w", f.Path, err)
		}
	}
	return nil
}

var errRollback = errors.New("formattest: rolled back")

// A deploy whose transaction rolls back leaves nothing: no item, no index
// and no queued work, which Env.Enqueue joins the transaction to promise.
func rollbackScenario(w *world) error {
	v := w.version("alpha", "1.0", 0)
	err := w.st.Tx(w.ctx, func(ctx context.Context) error {
		for _, f := range v.Files {
			w.tick()
			if _, err := w.items.Deploy(ctx, item.Deploy{
				Target: item.Target{Repo: w.a, Path: f.Path}, Body: bytes.NewReader(f.Body),
				By: "formattest",
			}); err != nil {
				return fmt.Errorf("deploy %s: %w", f.Path, err)
			}
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		return fmt.Errorf("the deploys inside the transaction failed: %w", err)
	}
	for _, f := range v.Files {
		if _, err := w.file(w.a, f.Path); err == nil {
			return fmt.Errorf("%w: %s survived its rollback", errBroken, f.Path)
		}
	}
	queued, err := w.work()
	if err != nil {
		return err
	}
	if len(queued) > 0 {
		return fmt.Errorf("%w: a rolled-back deploy left queued work %v", errBroken, queued)
	}
	return errors.Join(w.expect(w.a), w.verify(w.a))
}

// Committed deploys leave exactly one queued item of each kind the format
// declares per package, however many versions they wrote, and none of any
// other kind.
func workScenario(w *world) error {
	vs, err := w.three(w.a)
	if err != nil {
		return err
	}
	pkgs := map[string]bool{}
	for _, v := range vs {
		pkgs[v.Package] = true
	}
	want := map[string]int64{}
	for _, k := range w.f.Work {
		want[k] = int64(len(pkgs))
	}
	got, err := w.work()
	if err != nil {
		return err
	}
	if !maps.Equal(got, want) {
		return fmt.Errorf("%w: queued work %v; want %v", errBroken, got, want)
	}
	return nil
}

// The deploy policy every format keeps (decision 0032): a blacked-out
// repository refuses deploys with 404, and a path outside the include or
// exclude patterns is refused with 409, on a deploy and as a copy target.
// A format that overrides Admit must still apply them.
func patternsScenario(w *world) error {
	v := w.version("alpha", "1.0", 0)
	first := v.Files[0].Path
	nothing, blackedOut := "formattest-nothing/**", true
	for _, c := range []struct {
		key    string
		tune   func(*repo.Config)
		status int
	}{
		{"formattest-excludes", func(c *repo.Config) { c.ExcludesPattern = &first }, http.StatusConflict},
		{"formattest-includes", func(c *repo.Config) { c.IncludesPattern = &nothing }, http.StatusConflict},
		{"formattest-blacked-out", func(c *repo.Config) { c.BlackedOut = &blackedOut }, http.StatusNotFound},
	} {
		target := w.repo(c.key, c.tune)
		if err := refused(w.deploy(target, v, nil), c.status); err != nil {
			return fmt.Errorf("%s, deploying %s: %w", c.key, paths(v), err)
		}
		if _, err := w.file(target, first); err == nil {
			return fmt.Errorf("%w: %s holds the refused %s", errBroken, c.key, first)
		}
		if err := errors.Join(w.expect(target), w.verify(target)); err != nil {
			return err
		}
	}
	if err := w.deploy(w.a, v, nil); err != nil {
		return err
	}
	excluding := w.repo(
		"formattest-copy-excludes",
		func(c *repo.Config) { c.ExcludesPattern = &first },
	)
	w.tick()
	res, err := w.items.Transfer(w.ctx, item.Transfer{
		From: item.Target{Repo: w.a}, To: item.Target{Repo: excluding}, By: "formattest",
	})
	if err != nil {
		return fmt.Errorf("a copy onto an excluded path: %w", err)
	}
	if res.Failed == 0 {
		return fmt.Errorf(
			"%w: a copy onto an excluded path failed nothing: %v",
			errBroken,
			res.Messages,
		)
	}
	if _, err := w.file(excluding, first); err == nil {
		return fmt.Errorf("%w: the copy landed the excluded %s", errBroken, first)
	}
	return w.verify(excluding)
}

// A remote repository's cache is indexed as a local repository is
// (decision 0018): what a download stores links its version, a reindex
// rebuilds the same index from the cache, and deleting from the cache
// unlinks it.
func cacheScenario(w *world) error {
	if w.f.NoRemoteCache {
		return nil
	}
	r := w.remote(keyRemote)
	vs := []Version{
		w.version("alpha", "1.0", 0), w.version("alpha", "2.0", 0), w.version("beta", "1.0", 0),
	}
	for _, v := range vs {
		if err := w.cache(r, v); err != nil {
			return err
		}
	}
	if err := errors.Join(w.expect(r, vs...), w.verify(r)); err != nil {
		return err
	}
	for _, f := range vs[1].Files {
		w.tick()
		if _, err := w.items.Delete(
			w.ctx,
			item.Target{Repo: r, Cached: true, Path: f.Path},
			"formattest",
		); err != nil {
			return fmt.Errorf("delete %s from the cache: %w", f.Path, err)
		}
		if err := w.verify(r); err != nil {
			return fmt.Errorf("after deleting %s from the cache: %w", f.Path, err)
		}
	}
	return errors.Join(w.expect(r, vs[0], vs[2]), w.verify(r))
}
