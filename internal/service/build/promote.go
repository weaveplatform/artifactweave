package build

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ErrNoTarget is a promotion to a repository that does not exist.
var ErrNoTarget = errors.New("build: the target repository does not exist")

// errAborted rolls a fail-fast promotion back once it meets an error or a
// warning.
var errAborted = errors.New("build: promotion aborted")

// Promotion promotes the latest run of a build number (decision 0014).
type Promotion struct {
	ProjectKey, Name, Number string
	// Status is recorded with the promotion. Without one no promotion is
	// recorded, as in Artifactory, though files still move and properties
	// are still set.
	Status, Comment string
	// CIUser is the CI server's user; empty records By.
	CIUser string
	// Timestamp is when the promotion was requested; zero records now.
	Timestamp time.Time
	// SourceRepo limits the artifacts, not the dependencies, to one
	// repository, as Artifactory does.
	SourceRepo string
	// TargetRepo receives the files, each at the path it had; empty moves
	// none. A virtual repository stands for its default deployment
	// repository.
	TargetRepo string
	Copy       bool
	// Artifacts and Dependencies select what is promoted; Scopes limits the
	// dependencies to those in any one of them, and none means every scope.
	Artifacts, Dependencies bool
	Scopes                  []string
	// Properties replace the values of their keys on every promoted file:
	// where it landed, or where it is without a target.
	Properties types.Properties
	DryRun     bool
	// FailFast aborts at the first error or warning, changing nothing.
	FailFast bool
	By       string
}

// PromotionResult is a promotion's report, in Artifactory's wording.
type PromotionResult struct {
	Messages []item.Message
	// Aborted is a fail-fast promotion that met an error or a warning.
	// Nothing was changed: what it had done was rolled back.
	Aborted bool
}

// Promote moves or copies the files of the latest run of a number to a
// target repository, sets properties on them and records the promotion, as
// one transaction. It needs deploy on the build. Each file needs what a
// copy or move of it needs, and one that lacks it is reported, not refused,
// as Artifactory reports it. ErrNotFound when there is no such run,
// ErrNoTarget when the target does not exist.
func (s *Service) Promote(ctx context.Context, p Promotion, a Access) (PromotionResult, error) {
	b, err := s.Store.Builds().Get(ctx, ProjectKey(p.ProjectKey), p.Name, p.Number, time.Time{})
	if err != nil {
		return PromotionResult{}, fmt.Errorf("build: get run: %w", err)
	}
	repoKey, path := b.DocumentItem()
	if !a.Build(repoKey, path, false, types.ActionWrite) {
		return PromotionResult{}, ErrForbidden
	}
	pr := &promoter{s: s, p: p, a: a, run: b}
	if p.TargetRepo != "" {
		if pr.target, err = s.promotionTarget(ctx, p.TargetRepo); err != nil {
			return PromotionResult{}, err
		}
	}
	if p.DryRun {
		err = pr.promote(ctx)
	} else {
		err = s.Store.Tx(ctx, func(ctx context.Context) error {
			if err := pr.promote(ctx); err != nil {
				return err
			}
			if pr.aborted() {
				return errAborted
			}
			return nil
		})
	}
	switch {
	case errors.Is(err, errAborted):
		// The lines that say what was done describe what the rollback
		// undid.
		kept := slices.DeleteFunc(pr.msgs, func(m item.Message) bool {
			return m.Level == item.LevelInfo
		})
		return PromotionResult{
			Messages: append(kept, item.Message{
				Level: item.LevelInfo,
				Text:  "Promotion aborted: nothing was moved, copied or changed.",
			}),
			Aborted: true,
		}, nil
	case err != nil:
		return PromotionResult{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return PromotionResult{Messages: pr.msgs, Aborted: pr.aborted()}, nil
}

// promotionTarget resolves where promoted files land: a local or federated
// repository, or a virtual repository's default deployment repository.
func (s *Service) promotionTarget(ctx context.Context, key string) (repo.Config, error) {
	c, err := s.Store.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return c, fmt.Errorf("%w: %s", ErrNoTarget, key)
	case err != nil:
		return c, fmt.Errorf("build: repository %s: %w", key, err)
	case c.Class == repo.Virtual && c.DefaultDeploymentRepo == "":
		return c, fmt.Errorf("%w: the virtual repository '%s' has no default deployment repository",
			ErrInvalid, key)
	case c.Class == repo.Virtual:
		return s.promotionTarget(ctx, c.DefaultDeploymentRepo)
	case c.Class != repo.Local && c.Class != repo.Federated:
		return c, fmt.Errorf(
			"%w: the target repository '%s' is not a local repository",
			ErrInvalid,
			key,
		)
	}
	return c, nil
}

// promoter carries one promotion's state.
type promoter struct {
	s      *Service
	p      Promotion
	a      Access
	run    types.Build
	target repo.Config
	msgs   []item.Message
}

// located is an item a promotion acts on, with its repository.
type located struct {
	repo repo.Config
	it   types.Item
}

func (pr *promoter) sayf(level, format string, args ...any) {
	pr.msgs = append(pr.msgs, item.Message{Level: level, Text: fmt.Sprintf(format, args...)})
}

// aborted reports a fail-fast promotion that has met an error or a warning.
func (pr *promoter) aborted() bool {
	return pr.p.FailFast && slices.ContainsFunc(pr.msgs, func(m item.Message) bool {
		return m.Level != item.LevelInfo
	})
}

func (pr *promoter) promote(ctx context.Context) error {
	files, err := pr.collect(ctx)
	if err != nil || pr.aborted() {
		return err
	}
	tag := files
	if pr.target.Key == "" {
		pr.sayf(item.LevelInfo, "Skipping build item relocation: no target repository selected.")
	} else if tag, err = pr.relocate(ctx, files); err != nil || pr.aborted() {
		return err
	}
	if len(pr.p.Properties) > 0 {
		if err := pr.tag(ctx, tag); err != nil || pr.aborted() {
			return err
		}
	}
	return pr.record(ctx)
}

// collect finds the run's files: each artifact's item that carries the
// run's build.name and build.number, in SourceRepo when one is named, and
// each dependency's item, wherever it is. A file found for nothing is
// reported as an error.
func (pr *promoter) collect(ctx context.Context) ([]located, error) {
	var out []located
	seen := map[int64]bool{}
	add := func(f located) {
		if !seen[f.it.ID] {
			seen[f.it.ID] = true
			out = append(out, f)
		}
	}
	label := fmt.Sprintf("of build '%s' #%s", pr.run.Name, pr.run.Number)
	// A file without a checksum is found nowhere and reported nowhere, as
	// Artifactory leaves it out.
	unsummed := func(bf types.BuildFile) bool { return bf.Checksums == (types.Checksums{}) }
	for _, m := range pr.run.Modules {
		if pr.p.Artifacts {
			for _, bf := range slices.DeleteFunc(slices.Clone(m.Artifacts), unsummed) {
				f, ok, err := pr.s.findFile(ctx, bf, pr.s.artifactOf(pr.run, pr.fromSource))
				if err != nil {
					return nil, err
				}
				if !ok {
					pr.sayf(item.LevelError, "Unable to find artifact '%s' %s", bf.Name, label)
					if pr.aborted() {
						return nil, nil
					}
					continue
				}
				add(f)
			}
		}
		if !pr.p.Dependencies {
			continue
		}
		for _, bf := range slices.DeleteFunc(slices.Clone(m.Dependencies), unsummed) {
			if len(pr.p.Scopes) > 0 &&
				!slices.ContainsFunc(bf.Scopes, func(s string) bool {
					return slices.Contains(pr.p.Scopes, s)
				}) {
				continue
			}
			f, ok, err := pr.s.findFile(ctx, bf, nil)
			if err != nil {
				return nil, err
			}
			if !ok {
				pr.sayf(item.LevelError, "Unable to find dependency '%s' %s", bf.ID, label)
				if pr.aborted() {
					return nil, nil
				}
				continue
			}
			add(f)
		}
	}
	return out, nil
}

// fromSource accepts the items in the promotion's source repository, or
// every item when it names none.
func (pr *promoter) fromSource(it types.Item) bool {
	return pr.p.SourceRepo == "" || it.ReportedRepoKey() == pr.p.SourceRepo
}

// findFile returns the item a build file is: of those holding its checksum
// that keep passes, the one with its name, else the first. A promotion and
// the build artifacts search both find files this way, as Artifactory's
// getBuildArtifactsFileInfos does (research 22 §3.6).
func (s *Service) findFile(
	ctx context.Context,
	bf types.BuildFile,
	keep func(context.Context, types.Item) (bool, error),
) (located, bool, error) {
	found, err := collect(s.Page, func(pg store.Page) ([]types.Item, string, error) {
		return s.Store.Items().FindByChecksum(ctx, bf.Checksums, pg)
	})
	if err != nil {
		return located{}, false, fmt.Errorf("build: find %s: %w", bf.Name, err)
	}
	var match types.Item
	for _, it := range found {
		if keep != nil {
			ok, err := keep(ctx, it)
			if err != nil {
				return located{}, false, err
			}
			if !ok {
				continue
			}
		}
		if match.ID == 0 || (match.Name != bf.Name && it.Name == bf.Name) {
			match = it
		}
	}
	if match.ID == 0 {
		return located{}, false, nil
	}
	c, err := s.Store.Repos().Get(ctx, match.RepoKey)
	if err != nil {
		return located{}, false, fmt.Errorf("build: repository %s: %w", match.RepoKey, err)
	}
	return located{repo: c, it: match}, true, nil
}

// artifactOf returns findFile's keep for run's artifacts as Artifactory
// finds them: the items that in passes (nil passes all) and that carry the
// run's build.name and build.number.
func (s *Service) artifactOf(
	run types.Build,
	in func(types.Item) bool,
) func(context.Context, types.Item) (bool, error) {
	return func(ctx context.Context, it types.Item) (bool, error) {
		if in != nil && !in(it) {
			return false, nil
		}
		props, err := s.Store.Items().Properties(ctx, it.ID)
		if err != nil {
			return false, fmt.Errorf("build: properties of %s: %w", it.FullPath(), err)
		}
		return props.Has("build.name", run.Name) && props.Has("build.number", run.Number), nil
	}
}

// relocate copies or moves each file to the target, at its own path, and
// returns the files that landed there, or would in a dry run.
func (pr *promoter) relocate(ctx context.Context, files []located) ([]located, error) {
	verb := "move"
	if pr.p.Copy {
		verb = "copy"
	}
	var landed []located
	for _, f := range files {
		full := f.it.FullPath()
		label := fmt.Sprintf("%s:%s", f.it.ReportedRepoKey(), full)
		switch {
		case f.repo.Key == pr.target.Key && !f.it.Cached:
			pr.sayf(item.LevelInfo, "%s is already in %s", label, pr.target.Key)
			landed = append(landed, f)
		case !pr.a.Item(f.repo, full, types.ActionRead) ||
			(!pr.p.Copy && !pr.a.Item(f.repo, full, types.ActionDelete)):
			pr.sayf(item.LevelError, "Cannot %s %s: no permission to %s it", verb, label, verb)
		case !pr.p.Copy && f.it.Cached:
			pr.sayf(
				item.LevelError,
				"Cannot %s %s: files cannot be moved out of a remote repository's cache",
				verb,
				label,
			)
		case !pr.a.Item(pr.target, full, types.ActionWrite):
			pr.sayf(item.LevelError, "Cannot %s %s: no permission to deploy to %s:%s",
				verb, label, pr.target.Key, full)
		default:
			res, err := pr.s.items().Transfer(ctx, item.Transfer{
				From: item.Target{Repo: f.repo, Cached: f.it.Cached, Path: full},
				To:   item.Target{Repo: pr.target, Path: full},
				Move: !pr.p.Copy, Dry: pr.p.DryRun, FailFast: pr.p.FailFast,
				CanOverwrite: pr.a.Item(pr.target, full, types.ActionDelete),
				By:           pr.p.By,
			})
			if errors.Is(err, item.ErrInvalidTransfer) {
				pr.sayf(item.LevelError, "Cannot %s %s: %s", verb, label,
					strings.TrimPrefix(err.Error(), item.ErrInvalidTransfer.Error()+": "))
				break
			}
			if err != nil {
				return nil, fmt.Errorf("build: %s %s: %w", verb, label, err)
			}
			pr.msgs = append(pr.msgs, res.Messages...)
			if res.Failed == 0 {
				landed = append(landed, located{repo: pr.target, it: types.Item{
					RepoKey: pr.target.Key, Path: f.it.Path, Name: f.it.Name,
				}})
			}
		}
		if pr.aborted() {
			return nil, nil
		}
	}
	return landed, nil
}

// tag sets the promotion's properties on files, through the item service so
// their formats' Annotated hooks run. A file the caller may not annotate is
// a warning, as in Artifactory; a dry run only checks.
func (pr *promoter) tag(ctx context.Context, files []located) error {
	items := pr.s.items()
	for _, f := range files {
		full := f.it.FullPath()
		if !pr.a.Item(f.repo, full, types.ActionAnnotate) {
			pr.sayf(item.LevelWarning, "User doesn't have permissions to annotate '%s:%s'",
				f.it.ReportedRepoKey(), full)
			if pr.aborted() {
				return nil
			}
			continue
		}
		if pr.p.DryRun {
			continue
		}
		it, err := pr.s.Store.Items().Get(ctx, f.repo.Key, f.it.Cached, full)
		if err != nil {
			return fmt.Errorf("build: get %s: %w", full, err)
		}
		if err := items.Annotate(ctx, f.repo, it, pr.p.Properties, nil); err != nil {
			return fmt.Errorf("build: properties of %s: %w", full, err)
		}
	}
	return nil
}

// record adds the promotion to the run, unless there is no status to
// record or the run is dry.
func (pr *promoter) record(ctx context.Context) error {
	if pr.p.Status == "" {
		pr.sayf(item.LevelInfo, "Skipping promotion status update: no status received.")
		return nil
	}
	if pr.p.DryRun {
		return nil
	}
	created := pr.p.Timestamp
	if created.IsZero() {
		created = pr.s.now()
	}
	ci := pr.p.CIUser
	if ci == "" {
		ci = pr.p.By
	}
	if _, err := pr.s.Store.Builds().AddPromotion(ctx, types.BuildPromotion{
		BuildID: pr.run.ID, Created: created.UTC(), CreatedBy: pr.p.By, Status: pr.p.Status,
		Comment: pr.p.Comment, CIUser: ci, SourceRepo: pr.p.SourceRepo, Repo: pr.target.Key,
	}); err != nil {
		return fmt.Errorf("build: record promotion: %w", err)
	}
	return nil
}
