package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// buildName identifies a build name within a project.
type buildName struct{ Project, Name string }

func (k buildName) String() string { return k.Project + "/" + k.Name }

// Builds implements store.Store.
func (s *Store) Builds() store.BuildStore { return buildStore{s} }

type buildStore struct{ s *Store }

func (b buildStore) runs() *table[int64, types.Build] {
	return tableOf[int64, types.Build](b.s, "builds")
}

func (b buildStore) promotions() *table[int64, types.BuildPromotion] {
	return tableOf[int64, types.BuildPromotion](b.s, "build_promotions")
}

func (b buildStore) retention() *table[buildName, types.BuildRetention] {
	return tableOf[buildName, types.BuildRetention](b.s, "build_retention")
}

// validBuild rejects a run the store cannot key.
func validBuild(r types.Build) error {
	if r.Name == "" || r.Number == "" || r.Started.IsZero() {
		return fmt.Errorf("build %q number %q: name, number and started are required: %w",
			r.Name, r.Number, store.ErrInvalid)
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
	return b.runs().all(func(_ int64, r types.Build) bool {
		return r.ProjectKey == project && r.Name == name
	}, order)
}

// delete removes a run with its promotions.
func (b buildStore) delete(id int64) {
	b.runs().del(id)
	b.promotions().
		deleteWhere(func(_ int64, p types.BuildPromotion) bool { return p.BuildID == id })
}

func (b buildStore) Put(ctx context.Context, r types.Build) (types.Build, error) {
	if err := validBuild(r); err != nil {
		return types.Build{}, err
	}
	r = store.NormaliseBuild(r)
	err := b.s.do(ctx, func() error {
		for _, cur := range b.named(r.ProjectKey, r.Name, nil) {
			if cur.Number == r.Number && cur.Started.Equal(r.Started) {
				return conflict(
					"build run",
					fmt.Sprintf("%s/%s/%s", buildName{r.ProjectKey, r.Name}, r.Number, r.Started),
				)
			}
		}
		r.ID = b.s.nextID("builds")
		b.runs().put(r.ID, r)
		return nil
	})
	if err != nil {
		return types.Build{}, err
	}
	return r, nil
}

func (b buildStore) Replace(ctx context.Context, r types.Build) error {
	r = store.NormaliseBuild(r)
	return b.s.do(ctx, func() error {
		cur, ok := b.runs().get(r.ID)
		if !ok {
			return notFound("build run", r.ID)
		}
		cur.URL, cur.Modified, cur.ModifiedBy = r.URL, r.Modified, r.ModifiedBy
		cur.Properties, cur.Modules, cur.Document = r.Properties, r.Modules, r.Document
		b.runs().put(cur.ID, cur)
		return nil
	})
}

func (b buildStore) Get(
	ctx context.Context, project, name, number string, started time.Time,
) (types.Build, error) {
	started = store.Normalise(started)
	var out types.Build
	err := b.s.do(ctx, func() error {
		for _, r := range b.named(project, name, runsNewestFirst) {
			if r.Number == number && (started.IsZero() || r.Started.Equal(started)) {
				out = r
				return nil
			}
		}
		return notFound("build run", fmt.Sprintf("%s/%s", buildName{project, name}, number))
	})
	return out, err
}

func (b buildStore) ByID(ctx context.Context, id int64) (types.Build, error) {
	var out types.Build
	err := b.s.do(ctx, func() error {
		var ok bool
		if out, ok = b.runs().get(id); !ok {
			return notFound("build run", id)
		}
		return nil
	})
	return out, err
}

func (b buildStore) Latest(ctx context.Context, project, name string) (types.Build, error) {
	var out types.Build
	err := b.s.do(ctx, func() error {
		runs := b.named(project, name, runsNewestFirst)
		if len(runs) == 0 {
			return notFound("build", buildName{project, name})
		}
		out = runs[0]
		return nil
	})
	return out, err
}

func (b buildStore) Runs(
	ctx context.Context, project, name string, p store.Page,
) ([]types.BuildRun, string, error) {
	var (
		out  []types.BuildRun
		next string
	)
	err := b.s.do(ctx, func() error {
		runs := make([]types.BuildRun, 0)
		for _, r := range b.named(project, name, runsNewestFirst) {
			runs = append(runs, r.Run())
		}
		var err error
		out, next, err = paginate(runs, p, true, func(r types.BuildRun) []any {
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
	err := b.s.do(ctx, func() error {
		latest := map[string]time.Time{}
		for _, r := range b.runs().rows {
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
		out, next, err = paginate(
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
	err := b.s.do(ctx, func() error {
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
	err := b.s.do(ctx, func() error {
		for _, r := range b.named(project, name, runsOldestFirst) {
			b.delete(r.ID)
			out = append(out, r.Run())
		}
		b.retention().del(buildName{project, name})
		return nil
	})
	return out, err
}

func (b buildStore) Rename(ctx context.Context, project, from, to string) error {
	return b.s.do(ctx, func() error {
		runs := b.named(project, from, nil)
		if len(runs) == 0 {
			return notFound("build", buildName{project, from})
		}
		if from == to {
			return nil
		}
		if len(b.named(project, to, nil)) > 0 {
			return conflict("build", buildName{project, to})
		}
		for _, r := range runs {
			r.Name = to
			b.runs().put(r.ID, r)
		}
		ret, ok := b.retention().get(buildName{project, from})
		b.retention().del(buildName{project, from})
		b.retention().del(buildName{project, to})
		if ok {
			ret.Name = to
			b.retention().put(buildName{project, to}, ret)
		}
		return nil
	})
}

func (b buildStore) AddPromotion(
	ctx context.Context,
	p types.BuildPromotion,
) (types.BuildPromotion, error) {
	err := b.s.do(ctx, func() error {
		if !b.runs().has(p.BuildID) {
			return notFound("build run", p.BuildID)
		}
		p.ID = b.s.nextID("build_promotions")
		b.promotions().put(p.ID, p)
		return nil
	})
	if err != nil {
		return types.BuildPromotion{}, err
	}
	return store.Normalise(p), nil
}

func (b buildStore) Promotions(ctx context.Context, buildID int64) ([]types.BuildPromotion, error) {
	var out []types.BuildPromotion
	err := b.s.do(ctx, func() error {
		if !b.runs().has(buildID) {
			return notFound("build run", buildID)
		}
		out = b.promotions().all(
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
	err := b.s.do(ctx, func() error {
		runs := b.runs().all(nil, func(x, y types.Build) int { return cmp.Compare(x.ID, y.ID) })
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
	return b.s.do(ctx, func() error {
		b.retention().put(buildName{r.ProjectKey, r.Name}, r)
		return nil
	})
}

func (b buildStore) Retention(
	ctx context.Context,
	project, name string,
) (types.BuildRetention, error) {
	var out types.BuildRetention
	err := b.s.do(ctx, func() error {
		var ok bool
		if out, ok = b.retention().get(buildName{project, name}); !ok {
			return notFound("build retention", buildName{project, name})
		}
		return nil
	})
	return out, err
}
