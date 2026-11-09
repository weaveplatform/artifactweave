package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// buildName identifies a build name within a project.
type buildName struct{ Project, Name string }

func (k buildName) String() string { return k.Project + "/" + k.Name }

// Builds implements store.Store.
func (s *Store) Builds() store.BuildStore { return buildStore{s} }

type buildStore struct{ s *Store }

func (b buildStore) runs() *memtx.Table[int64, types.Build] {
	return memtx.TableOf[int64, types.Build](b.s.Store, "builds")
}

func (b buildStore) promotions() *memtx.Table[int64, types.BuildPromotion] {
	return memtx.TableOf[int64, types.BuildPromotion](b.s.Store, "build_promotions")
}

func (b buildStore) retention() *memtx.Table[buildName, types.BuildRetention] {
	return memtx.TableOf[buildName, types.BuildRetention](b.s.Store, "build_retention")
}

// validBuild rejects a run the store cannot key.
func validBuild(r types.Build) error {
	if r.Name == "" || r.Number == "" || r.Started.IsZero() {
		return fmt.Errorf("build %q number %q: name, number and started are required: %w",
			r.Name, r.Number, store.ErrInvalid)
	}
	if err := store.CheckBuildPropertyKeys(r); err != nil {
		return fmt.Errorf("build %q number %q: %w", r.Name, r.Number, err)
	}
	return nil
}

// runsNewestFirst orders runs by (started, ID) descending.
func runsNewestFirst(x, y types.Build) int {
	if c := y.Started.Compare(x.Started); c != 0 {
		return c
	}
	return cmp.Compare(y.ID, x.ID)
}

// runsOldestFirst orders runs by (started, ID) ascending.
func runsOldestFirst(x, y types.Build) int { return runsNewestFirst(y, x) }

// named returns the runs of a name, sorted by order.
func (b buildStore) named(project, name string, order func(x, y types.Build) int) []types.Build {
	return b.runs().All(func(_ int64, r types.Build) bool {
		return r.ProjectKey == project && r.Name == name
	}, order)
}

// delete removes a run with its promotions.
func (b buildStore) delete(id int64) {
	b.runs().Del(id)
	b.promotions().
		DeleteWhere(func(_ int64, p types.BuildPromotion) bool { return p.BuildID == id })
}

func (b buildStore) Put(ctx context.Context, r types.Build) (types.Build, error) {
	if err := validBuild(r); err != nil {
		return types.Build{}, err
	}
	r = store.NormaliseBuild(r)
	err := b.s.Do(ctx, func() error {
		for _, cur := range b.named(r.ProjectKey, r.Name, nil) {
			if cur.Number == r.Number && cur.Started.Equal(r.Started) {
				return memtx.Conflict(
					"build run",
					fmt.Sprintf("%s/%s/%s", buildName{r.ProjectKey, r.Name}, r.Number, r.Started),
				)
			}
		}
		r.ID = b.s.NextID("builds")
		b.runs().Put(r.ID, r)
		return nil
	})
	if err != nil {
		return types.Build{}, err
	}
	return r, nil
}

func (b buildStore) Replace(ctx context.Context, r types.Build) error {
	if err := store.CheckBuildPropertyKeys(r); err != nil {
		return fmt.Errorf("build run %d: %w", r.ID, err)
	}
	r = store.NormaliseBuild(r)
	return b.s.Do(ctx, func() error {
		cur, ok := b.runs().Get(r.ID)
		if !ok {
			return memtx.NotFound("build run", r.ID)
		}
		cur.URL, cur.Modified, cur.ModifiedBy = r.URL, r.Modified, r.ModifiedBy
		cur.Properties, cur.Modules, cur.Document = r.Properties, r.Modules, r.Document
		b.runs().Put(cur.ID, cur)
		return nil
	})
}

func (b buildStore) Get(
	ctx context.Context, project, name, number string, started time.Time,
) (types.Build, error) {
	started = store.Normalise(started)
	var out types.Build
	err := b.s.Do(ctx, func() error {
		for _, r := range b.named(project, name, runsNewestFirst) {
			if r.Number == number && (started.IsZero() || r.Started.Equal(started)) {
				out = r
				return nil
			}
		}
		return memtx.NotFound("build run", fmt.Sprintf("%s/%s", buildName{project, name}, number))
	})
	return out, err
}

func (b buildStore) ByID(ctx context.Context, id int64) (types.Build, error) {
	var out types.Build
	err := b.s.Do(ctx, func() error {
		var ok bool
		if out, ok = b.runs().Get(id); !ok {
			return memtx.NotFound("build run", id)
		}
		return nil
	})
	return out, err
}

func (b buildStore) GetForUpdate(
	ctx context.Context,
	project, name, number string,
	started time.Time,
) (types.Build, error) {
	if !b.s.InTx(ctx) {
		return types.Build{}, fmt.Errorf(
			"get build for update requires a transaction: %w",
			store.ErrInvalid,
		)
	}
	return b.Get(ctx, project, name, number, started)
}

func (b buildStore) Latest(ctx context.Context, project, name string) (types.Build, error) {
	var out types.Build
	err := b.s.Do(ctx, func() error {
		runs := b.named(project, name, runsNewestFirst)
		if len(runs) == 0 {
			return memtx.NotFound("build", buildName{project, name})
		}
		out = runs[0]
		return nil
	})
	return out, err
}

func (b buildStore) ByIDForUpdate(ctx context.Context, id int64) (types.Build, error) {
	if !b.s.InTx(ctx) {
		return types.Build{}, fmt.Errorf(
			"get build by ID for update requires a transaction: %w",
			store.ErrInvalid,
		)
	}
	return b.ByID(ctx, id)
}

func (b buildStore) Runs(
	ctx context.Context, project, name string, p store.Page,
) ([]types.BuildRun, string, error) {
	var (
		out  []types.BuildRun
		next string
	)
	err := b.s.Do(ctx, func() error {
		runs := make([]types.BuildRun, 0)
		for _, r := range b.named(project, name, runsNewestFirst) {
			runs = append(runs, r.Run())
		}
		var err error
		out, next, err = memtx.Paginate(runs, p, true, func(r types.BuildRun) []any {
			return []any{r.Started.UnixMicro(), r.ID}
		})
		return err
	})
	return out, next, err
}

func (b buildStore) Names(
	ctx context.Context,
	project string,
	p store.Page,
) ([]types.BuildName, string, error) {
	var (
		out  []types.BuildName
		next string
	)
	err := b.s.Do(ctx, func() error {
		latest := map[string]time.Time{}
		for _, r := range b.runs().Rows {
			if r.ProjectKey == project && r.Started.After(latest[r.Name]) {
				latest[r.Name] = r.Started
			}
		}
		names := make([]types.BuildName, 0, len(latest))
		for n, at := range latest {
			names = append(names, types.BuildName{ProjectKey: project, Name: n, LastStarted: at})
		}
		slices.SortFunc(
			names,
			func(x, y types.BuildName) int { return cmp.Compare(x.Name, y.Name) },
		)
		var err error
		out, next, err = memtx.Paginate(
			names,
			p,
			false,
			func(n types.BuildName) []any { return []any{n.Name} },
		)
		return err
	})
	return out, next, err
}

func (b buildStore) DeleteNumbers(
	ctx context.Context, project, name string, numbers []string,
) ([]types.BuildRun, error) {
	var out []types.BuildRun
	err := b.s.Do(ctx, func() error {
		for _, number := range numbers {
			for _, r := range b.named(project, name, runsOldestFirst) {
				if r.Number == number {
					b.delete(r.ID)
					out = append(out, r.Run())
					break
				}
			}
		}
		return nil
	})
	return out, err
}

func (b buildStore) DeleteAll(ctx context.Context, project, name string) ([]types.BuildRun, error) {
	var out []types.BuildRun
	err := b.s.Do(ctx, func() error {
		for _, r := range b.named(project, name, runsOldestFirst) {
			b.delete(r.ID)
			out = append(out, r.Run())
		}
		b.retention().Del(buildName{project, name})
		return nil
	})
	return out, err
}

func (b buildStore) Rename(ctx context.Context, project, from, to string) error {
	return b.s.Do(ctx, func() error {
		runs := b.named(project, from, nil)
		if len(runs) == 0 {
			return memtx.NotFound("build", buildName{project, from})
		}
		if from == to {
			return nil
		}
		if len(b.named(project, to, nil)) > 0 {
			return memtx.Conflict("build", buildName{project, to})
		}
		for _, r := range runs {
			r.Name = to
			b.runs().Put(r.ID, r)
		}
		ret, ok := b.retention().Get(buildName{project, from})
		b.retention().Del(buildName{project, from})
		b.retention().Del(buildName{project, to})
		if ok {
			ret.Name = to
			b.retention().Put(buildName{project, to}, ret)
		}
		return nil
	})
}

func (b buildStore) AddPromotion(
	ctx context.Context,
	p types.BuildPromotion,
) (types.BuildPromotion, error) {
	err := b.s.Do(ctx, func() error {
		if !b.runs().Has(p.BuildID) {
			return memtx.NotFound("build run", p.BuildID)
		}
		p.ID = b.s.NextID("build_promotions")
		b.promotions().Put(p.ID, p)
		return nil
	})
	if err != nil {
		return types.BuildPromotion{}, err
	}
	return store.Normalise(p), nil
}

func (b buildStore) Promotions(ctx context.Context, buildID int64) ([]types.BuildPromotion, error) {
	var out []types.BuildPromotion
	err := b.s.Do(ctx, func() error {
		if !b.runs().Has(buildID) {
			return memtx.NotFound("build run", buildID)
		}
		out = b.promotions().All(
			func(_ int64, p types.BuildPromotion) bool { return p.BuildID == buildID },
			func(x, y types.BuildPromotion) int {
				if c := x.Created.Compare(y.Created); c != 0 {
					return c
				}
				return cmp.Compare(x.ID, y.ID)
			})
		return nil
	})
	return out, err
}

// validChecksums rejects a lookup that would match every file.
func validChecksums(c types.Checksums) error {
	if c.SHA1 == "" && c.SHA256 == "" && c.MD5 == "" {
		return fmt.Errorf("build file lookup needs a checksum: %w", store.ErrInvalid)
	}
	return nil
}

// checksumsMatch reports whether got equals every non-empty field of want.
func checksumsMatch(want, got types.Checksums) bool {
	return (want.SHA1 == "" || want.SHA1 == got.SHA1) &&
		(want.SHA256 == "" || want.SHA256 == got.SHA256) &&
		(want.MD5 == "" || want.MD5 == got.MD5)
}

// find returns the files of every run, selected from each module by files,
// whose checksums match c.
func (b buildStore) find(
	ctx context.Context, c types.Checksums, files func(types.BuildModule) []types.BuildFile,
) ([]types.BuildFileRef, error) {
	if err := validChecksums(c); err != nil {
		return nil, err
	}
	var out []types.BuildFileRef
	err := b.s.Do(ctx, func() error {
		runs := b.runs().All(nil, func(x, y types.Build) int { return cmp.Compare(x.ID, y.ID) })
		for _, r := range runs {
			for _, m := range r.Modules {
				for _, f := range files(m) {
					if checksumsMatch(c, f.Checksums) {
						out = append(out, types.BuildFileRef{Run: r.Run(), Module: m.ID, File: f})
					}
				}
			}
		}
		return nil
	})
	return out, err
}

func (b buildStore) FindArtifacts(
	ctx context.Context,
	c types.Checksums,
) ([]types.BuildFileRef, error) {
	return b.find(ctx, c, func(m types.BuildModule) []types.BuildFile { return m.Artifacts })
}

func (b buildStore) FindDependencies(
	ctx context.Context,
	c types.Checksums,
) ([]types.BuildFileRef, error) {
	return b.find(ctx, c, func(m types.BuildModule) []types.BuildFile { return m.Dependencies })
}

func (b buildStore) PutRetention(ctx context.Context, r types.BuildRetention) error {
	return b.s.Do(ctx, func() error {
		b.retention().Put(buildName{r.ProjectKey, r.Name}, r)
		return nil
	})
}

func (b buildStore) Retention(
	ctx context.Context,
	project, name string,
) (types.BuildRetention, error) {
	var out types.BuildRetention
	err := b.s.Do(ctx, func() error {
		var ok bool
		if out, ok = b.retention().Get(buildName{project, name}); !ok {
			return memtx.NotFound("build retention", buildName{project, name})
		}
		return nil
	})
	return out, err
}
