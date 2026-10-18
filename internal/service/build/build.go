// Package build implements the Builds API (decision 0014): publishing,
// reading, appending to, renaming and deleting build-info runs, and finding
// the items a run's artifacts are. A run is a store record and a document
// item in its project's build-info repository. Every change writes both in
// one transaction, so the Builds API, the storage API and AQL agree.
package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	// ErrInvalid is a document or request the API refuses as malformed.
	ErrInvalid = errors.New("build: invalid request")
	// ErrNotFound is a build or run that does not exist.
	ErrNotFound = store.ErrNotFound
	// ErrNoRepository is a project whose build-info repository does not
	// exist. Projects are P11, so only the default project's is seeded.
	ErrNoRepository = errors.New("build: the build-info repository does not exist")
	// ErrForbidden is an operation the caller's permissions do not allow.
	ErrForbidden = errors.New("build: permission denied")
	// ErrConflict is a rename to a name that has runs.
	ErrConflict = store.ErrConflict
)

// Access answers what the caller may do. Builds are the build resource of
// the permission model, matched against the paths of their document items
// in the build-info repository (decision 0014): a run's document file, or
// a build name's folder for a listing of names. Deleting a run's artifacts
// asks about each of their items as an artifact.
type Access interface {
	Build(repoKey, path string, folder bool, want types.Actions) bool
	Item(c repo.Config, path string, want types.Actions) bool
}

// Service works on the builds of one store.
type Service struct {
	Store store.Store
	Blobs blob.Store
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	// Page bounds the store pages a listing reads; zero means
	// store.MaxLimit.
	Page int
	// Formats and Work reach the item service's format hooks (decision
	// 0032); nil means formats.For and the store's queue.
	Formats func(repo.PackageType) format.Handler
	Work    item.Enqueuer
	// AllowDuplicateEntries accepts a document whose module lists one file
	// twice, which the zero value refuses as Artifactory 7.x does by default
	// (build.block_duplicate_entries).
	AllowDuplicateEntries bool
}

// checkDuplicates refuses modules that list one file twice, unless the
// service allows it.
func (s *Service) checkDuplicates(modules []types.BuildModule) error {
	if s.AllowDuplicateEntries {
		return nil
	}
	return duplicateEntry(modules)
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) items() *item.Service {
	return &item.Service{
		Store: s.Store, Blobs: s.Blobs, Now: s.Now, Formats: s.Formats, Work: s.Work,
	}
}

// ProjectKey is the store's key for a project parameter: "" for the default
// project, which clients may also name "default".
func ProjectKey(p string) string {
	if p == "default" {
		return ""
	}
	return p
}

// Published is what a publish stored.
type Published struct {
	Run types.Build
	// SHA256 is the stored document's, which the JFrog CLI reports.
	SHA256 string
	// Discarded are the runs the document's buildRetention discarded.
	Discarded []types.BuildRun
	// Retention is why the document's buildRetention discarded nothing,
	// such as a publisher without delete. The publish stands regardless,
	// as Artifactory's does.
	Retention error
}

// Publish stores the run a build-info document publishes, and the document
// as its item. Publishing needs deploy. A run with the same identity
// (project, name, number and started) is replaced, which, like overwriting
// an existing document item, needs delete as well. A buildRetention in the
// document is kept as the build name's retention and applied once the run
// is stored, as the publisher (decision 0014). A module that lists one file
// twice is ErrInvalid unless AllowDuplicateEntries.
func (s *Service) Publish(
	ctx context.Context,
	projectKey string,
	doc []byte,
	by string,
	a Access,
) (Published, error) {
	projectKey = ProjectKey(projectKey)
	b, err := Parse(projectKey, doc)
	if err != nil {
		return Published{}, err
	}
	if err := s.checkDuplicates(b.Modules); err != nil {
		return Published{}, err
	}
	retention, retains, err := documentRetention(doc)
	if err != nil {
		return Published{}, err
	}
	retention.ProjectKey, retention.Name = projectKey, b.Name
	c, err := s.repository(ctx, projectKey)
	if err != nil {
		return Published{}, err
	}
	_, path := b.DocumentItem()
	if !a.Build(c.Key, path, false, types.ActionWrite) {
		return Published{}, ErrForbidden
	}
	now := s.now()
	b.Created, b.CreatedBy, b.Modified, b.ModifiedBy = now, by, now, by
	var out Published
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		old, err := s.Store.Builds().Get(ctx, projectKey, b.Name, b.Number, b.Started)
		switch {
		case err == nil:
			b.ID, b.Created, b.CreatedBy = old.ID, old.Created, old.CreatedBy
		case !errors.Is(err, store.ErrNotFound):
			return fmt.Errorf("build: get run: %w", err)
		}
		exists, err := s.exists(ctx, c, path)
		if err != nil {
			return err
		}
		if (b.ID != 0 || exists) && !a.Build(c.Key, path, false, types.ActionDelete) {
			return ErrForbidden
		}
		if b.ID != 0 {
			err = s.Store.Builds().Replace(ctx, b)
		} else {
			b, err = s.Store.Builds().Put(ctx, b)
		}
		if err != nil {
			return fmt.Errorf("build: store run: %w", err)
		}
		it, err := s.writeDocument(ctx, c, b, by)
		out = Published{Run: b, SHA256: it.Actual.SHA256}
		if err != nil || !retains {
			return err
		}
		if err := s.Store.Builds().PutRetention(ctx, retention); err != nil {
			return fmt.Errorf("build: store retention: %w", err)
		}
		return nil
	})
	if err != nil {
		return Published{}, err //nolint:wrapcheck // the transaction returns ours
	}
	if retains && (retention.Count > 0 || !retention.MinimumBuildDate.IsZero()) {
		out.Discarded, out.Retention = s.Discard(ctx, Discard{
			ProjectKey: projectKey, Name: b.Name, Retention: retention, By: by,
		}, a)
	}
	return out, nil
}

// Run returns a run of a build number: the one started at started, or the
// latest-started when started is zero.
func (s *Service) Run(
	ctx context.Context,
	projectKey, name, number string,
	started time.Time,
	a Access,
) (types.Build, error) {
	b, err := s.Store.Builds().Get(ctx, ProjectKey(projectKey), name, number, started)
	if err != nil {
		return types.Build{}, fmt.Errorf("build: get run: %w", err)
	}
	repoKey, path := b.DocumentItem()
	if !a.Build(repoKey, path, false, types.ActionRead) {
		return types.Build{}, ErrForbidden
	}
	return b, nil
}

// Runs lists the runs of a build the caller may read, newest first.
// ErrNotFound when the build has no runs, ErrForbidden when the caller may
// read none.
func (s *Service) Runs(
	ctx context.Context,
	projectKey, name string,
	a Access,
) ([]types.BuildRun, error) {
	projectKey = ProjectKey(projectKey)
	all, err := s.runs(ctx, projectKey, name)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("build: runs of %s: %w", name, ErrNotFound)
	}
	repoKey := types.BuildInfoRepo(projectKey)
	var out []types.BuildRun
	for _, r := range all {
		if a.Build(repoKey, documentPath(r), false, types.ActionRead) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, ErrForbidden
	}
	return out, nil
}

// Names lists the build names of a project in which the caller may read a
// run, ordered by name.
func (s *Service) Names(
	ctx context.Context,
	projectKey string,
	a Access,
) ([]types.BuildName, error) {
	projectKey = ProjectKey(projectKey)
	all, err := collect(s.Page, func(p store.Page) ([]types.BuildName, string, error) {
		return s.Store.Builds().Names(ctx, projectKey, p)
	})
	if err != nil {
		return nil, fmt.Errorf("build: names: %w", err)
	}
	repoKey := types.BuildInfoRepo(projectKey)
	var out []types.BuildName
	for _, n := range all {
		if a.Build(repoKey, types.EncodeBuildInfoSegment(n.Name), true, types.ActionRead) {
			out = append(out, n)
		}
	}
	return out, nil
}

// Append adds modules to a run.
type Append struct {
	ProjectKey, Name, Number string
	// Started selects the run; zero selects the number's latest.
	Started time.Time
	// Modules is the request body: a JSON array of build-info modules.
	Modules []byte
	By      string
}

// Append adds modules to the end of a run's modules. It rewrites the run's
// document and its item, so, like a publish that replaces one, it needs
// deploy and delete. The added modules are checked for duplicate entries as
// a publish's are; the run's own are not, as it may have been published
// while they were allowed.
func (s *Service) Append(ctx context.Context, ap Append, a Access) error {
	var added []json.RawMessage
	if err := json.Unmarshal(ap.Modules, &added); err != nil {
		return fmt.Errorf("%w: the body is not a JSON array of modules: %w", ErrInvalid, err)
	}
	projectKey := ProjectKey(ap.ProjectKey)
	c, err := s.repository(ctx, projectKey)
	if err != nil {
		return err
	}
	return s.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // ours
		b, err := s.Store.Builds().Get(ctx, projectKey, ap.Name, ap.Number, ap.Started)
		if err != nil {
			return fmt.Errorf("build: get run: %w", err)
		}
		_, path := b.DocumentItem()
		if !a.Build(c.Key, path, false, types.ActionWrite|types.ActionDelete) {
			return ErrForbidden
		}
		var cur struct {
			Modules []json.RawMessage `json:"modules"`
		}
		if err := json.Unmarshal(b.Document, &cur); err != nil {
			return fmt.Errorf("%w: the stored build-info document: %w", ErrInvalid, err)
		}
		modules, err := json.Marshal(append(cur.Modules, added...))
		if err != nil {
			return fmt.Errorf("build: modules: %w", err)
		}
		doc, err := setFields(b.Document, map[string]json.RawMessage{"modules": modules})
		if err != nil {
			return err
		}
		merged, err := Parse(projectKey, doc)
		if err != nil {
			return err
		}
		if err := s.checkDuplicates(merged.Modules[len(cur.Modules):]); err != nil {
			return err
		}
		b.Modules, b.Properties, b.Document = merged.Modules, merged.Properties, doc
		b.Modified, b.ModifiedBy = s.now(), ap.By
		if err := s.Store.Builds().Replace(ctx, b); err != nil {
			return fmt.Errorf("build: replace run: %w", err)
		}
		_, err = s.writeDocument(ctx, c, b, ap.By)
		return err
	})
}

// Rename moves every run of a build to another name, with its document,
// whose name field changes too. It needs deploy and delete on the runs, and
// deploy where they land.
func (s *Service) Rename(ctx context.Context, projectKey, from, to, by string, a Access) error {
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("%w: the new build name is empty", ErrInvalid)
	}
	projectKey = ProjectKey(projectKey)
	c, err := s.repository(ctx, projectKey)
	if err != nil {
		return err
	}
	name, err := json.Marshal(to)
	if err != nil {
		return fmt.Errorf("build: name: %w", err)
	}
	return s.Store.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // ours
		runs, err := s.runs(ctx, projectKey, from)
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			return fmt.Errorf("build: runs of %s: %w", from, ErrNotFound)
		}
		for _, r := range runs {
			moved := r
			moved.Name = to
			if !a.Build(c.Key, documentPath(r), false, types.ActionWrite|types.ActionDelete) ||
				!a.Build(c.Key, documentPath(moved), false, types.ActionWrite) {
				return ErrForbidden
			}
		}
		if from == to {
			return nil
		}
		if err := s.Store.Builds().Rename(ctx, projectKey, from, to); err != nil {
			return fmt.Errorf("build: rename %s: %w", from, err)
		}
		for _, r := range runs {
			b, err := s.Store.Builds().ByID(ctx, r.ID)
			if err != nil {
				return fmt.Errorf("build: get run: %w", err)
			}
			if b.Document, err = setFields(
				b.Document,
				map[string]json.RawMessage{"name": name},
			); err != nil {
				return err
			}
			b.Modified, b.ModifiedBy = s.now(), by
			if err := s.Store.Builds().Replace(ctx, b); err != nil {
				return fmt.Errorf("build: replace run: %w", err)
			}
			if _, err := s.writeDocument(ctx, c, b, by); err != nil {
				return err
			}
			// Names that differ only in whitespace encode alike, and then
			// the document just written is where the old one was.
			if _, path := b.DocumentItem(); path != documentPath(r) {
				if err := s.dropDocument(ctx, c, r); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Delete selects runs of a build to delete.
type Delete struct {
	ProjectKey, Name string
	// Numbers deletes one run per occurrence of a number.
	Numbers []string
	// All deletes every run, ignoring Numbers.
	All bool
	// Artifacts deletes the items the runs' artifacts are, too.
	Artifacts bool
	By        string
}

// Delete deletes runs of a build: every run with All, otherwise one run per
// occurrence of a number in Numbers, the earliest-started first, which is
// what jf rt bp --overwrite expects (research 22 §3.3). Each run's document
// item goes with it. With Artifacts, so do the items holding an artifact's
// checksum that carry the run's build.name and build.number properties, as
// item deletes: to the trash can, and needing delete on each. It returns the
// deleted runs; ErrNotFound when none was.
func (s *Service) Delete(ctx context.Context, d Delete, a Access) ([]types.BuildRun, error) {
	if !d.All && len(d.Numbers) == 0 {
		return nil, fmt.Errorf("%w: no build numbers to delete", ErrInvalid)
	}
	projectKey := ProjectKey(d.ProjectKey)
	c, err := s.repository(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	var gone []types.BuildRun
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		// The store returns the deleted runs without their modules, so the
		// artifacts are read first.
		loaded := map[int64]types.Build{}
		if d.Artifacts {
			runs, err := s.runs(ctx, projectKey, d.Name)
			if err != nil {
				return err
			}
			for _, r := range runs {
				if loaded[r.ID], err = s.Store.Builds().ByID(ctx, r.ID); err != nil {
					return fmt.Errorf("build: get run: %w", err)
				}
			}
		}
		var err error
		if d.All {
			gone, err = s.Store.Builds().DeleteAll(ctx, projectKey, d.Name)
		} else {
			gone, err = s.Store.Builds().DeleteNumbers(ctx, projectKey, d.Name, d.Numbers)
		}
		if err != nil {
			return fmt.Errorf("build: delete runs of %s: %w", d.Name, err)
		}
		if len(gone) == 0 {
			return fmt.Errorf("build: runs of %s: %w", d.Name, ErrNotFound)
		}
		for _, r := range gone {
			if !a.Build(c.Key, documentPath(r), false, types.ActionDelete) {
				return ErrForbidden
			}
		}
		for _, r := range gone {
			if err := s.dropDocument(ctx, c, r); err != nil {
				return err
			}
			if d.Artifacts {
				if err := s.deleteArtifacts(ctx, loaded[r.ID], d.By, a); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // the transaction returns ours
	}
	return gone, nil
}

// Artifacts returns the items the artifacts of a number's latest run are:
// the files holding an artifact's checksum that the caller may read. Like
// the AQL the JFrog CLI otherwise runs, it matches by checksum alone; the
// CLI ranks the matches by their build properties itself (research 22
// §3.1).
func (s *Service) Artifacts(
	ctx context.Context,
	projectKey, name, number string,
	a Access,
) ([]types.Item, error) {
	b, err := s.Run(ctx, projectKey, name, number, time.Time{}, a)
	if err != nil {
		return nil, err
	}
	var out []types.Item
	err = s.eachArtifactItem(ctx, b, func(c repo.Config, it types.Item) error {
		if a.Item(c, it.FullPath(), types.ActionRead) {
			out = append(out, it)
		}
		return nil
	})
	return out, err
}

// SearchArtifacts returns the items run b's artifacts are that the caller
// may read, in module and artifact order, found as a promotion finds them:
// of the items that in passes (nil passes all), hold an artifact's checksum
// and carry the run's build properties, the one with its name, else the
// first. Artifacts, which the JFrog CLI ranks itself, matches by checksum
// alone.
func (s *Service) SearchArtifacts(
	ctx context.Context,
	b types.Build,
	in func(types.Item) bool,
	a Access,
) ([]types.Item, error) {
	keep := s.artifactOf(b, in)
	seen, taken := map[int64]bool{}, claims{}
	var out []types.Item
	for _, m := range b.Modules {
		for _, bf := range m.Artifacts {
			if bf.Checksums == (types.Checksums{}) {
				continue
			}
			f, ok, err := s.findFile(ctx, bf, keep, taken, artifactKey(bf))
			if err != nil {
				return nil, err
			}
			if !ok || seen[f.it.ID] || !a.Item(f.repo, f.it.FullPath(), types.ActionRead) {
				continue
			}
			seen[f.it.ID] = true
			out = append(out, f.it)
		}
	}
	return out, nil
}

// Latest is the build number that stands for a build's most recently
// started run.
const Latest = "LATEST"

// Find returns the most recently started run of a build number, or of the
// build when number is Latest, whose latest promotion has status when status
// is not empty, skipping runs the caller may not read. ErrNotFound when no
// run qualifies, ErrForbidden when the caller may read none that does.
func (s *Service) Find(
	ctx context.Context,
	projectKey, name, number, status string,
	a Access,
) (types.Build, error) {
	projectKey = ProjectKey(projectKey)
	all, err := s.runs(ctx, projectKey, name)
	if err != nil {
		return types.Build{}, err
	}
	repoKey, hidden := types.BuildInfoRepo(projectKey), false
	for _, r := range all {
		if number != Latest && r.Number != number {
			continue
		}
		if status != "" {
			ok, err := s.promotedTo(ctx, r.ID, status)
			if err != nil {
				return types.Build{}, err
			}
			if !ok {
				continue
			}
		}
		if !a.Build(repoKey, documentPath(r), false, types.ActionRead) {
			hidden = true
			continue
		}
		b, err := s.Store.Builds().ByID(ctx, r.ID)
		if err != nil {
			return types.Build{}, fmt.Errorf("build: get run: %w", err)
		}
		return b, nil
	}
	if hidden {
		return types.Build{}, ErrForbidden
	}
	return types.Build{}, fmt.Errorf("build: run %s/%s: %w", name, number, ErrNotFound)
}

// promotedTo reports whether run id's latest promotion has status.
func (s *Service) promotedTo(ctx context.Context, id int64, status string) (bool, error) {
	ps, err := s.Store.Builds().Promotions(ctx, id)
	if err != nil {
		return false, fmt.Errorf("build: promotions of run %d: %w", id, err)
	}
	return len(ps) > 0 && ps[len(ps)-1].Status == status, nil
}

// deleteArtifacts deletes the items of run b's artifacts that carry its
// build properties.
func (s *Service) deleteArtifacts(ctx context.Context, b types.Build, by string, a Access) error {
	return s.eachArtifactItem(ctx, b, func(c repo.Config, it types.Item) error {
		props, err := s.Store.Items().Properties(ctx, it.ID)
		if err != nil {
			return fmt.Errorf("build: properties of %s: %w", it.FullPath(), err)
		}
		if !props.Has("build.name", b.Name) || !props.Has("build.number", b.Number) {
			return nil
		}
		if !a.Item(c, it.FullPath(), types.ActionDelete) {
			return ErrForbidden
		}
		if _, err := s.items().Delete(
			ctx,
			item.Target{Repo: c, Cached: it.Cached, Path: it.FullPath()},
			by,
		); err != nil {
			return fmt.Errorf("build: delete artifact %s: %w", it.FullPath(), err)
		}
		return nil
	})
}

// eachArtifactItem calls fn once for each file holding the checksum of one
// of b's artifacts, with its repository.
func (s *Service) eachArtifactItem(
	ctx context.Context,
	b types.Build,
	fn func(repo.Config, types.Item) error,
) error {
	seen := map[int64]bool{}
	repos := map[string]repo.Config{}
	for _, m := range b.Modules {
		for _, f := range m.Artifacts {
			if f.Checksums == (types.Checksums{}) {
				continue
			}
			found, err := collect(s.Page, func(p store.Page) ([]types.Item, string, error) {
				return s.Store.Items().FindByChecksum(ctx, f.Checksums, p)
			})
			if err != nil {
				return fmt.Errorf("build: find artifact %s: %w", f.Name, err)
			}
			for _, it := range found {
				if seen[it.ID] {
					continue
				}
				seen[it.ID] = true
				c, ok := repos[it.RepoKey]
				if !ok {
					if c, err = s.Store.Repos().Get(ctx, it.RepoKey); err != nil {
						return fmt.Errorf("build: repository %s: %w", it.RepoKey, err)
					}
					repos[it.RepoKey] = c
				}
				if err := fn(c, it); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// repository returns a project's build-info repository.
func (s *Service) repository(ctx context.Context, projectKey string) (repo.Config, error) {
	key := types.BuildInfoRepo(projectKey)
	c, err := s.Store.Repos().Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return c, fmt.Errorf("%w: %s", ErrNoRepository, key)
	}
	if err != nil {
		return c, fmt.Errorf("build: repository %s: %w", key, err)
	}
	return c, nil
}

// runs lists every run of a build, newest first.
func (s *Service) runs(ctx context.Context, projectKey, name string) ([]types.BuildRun, error) {
	out, err := collect(s.Page, func(p store.Page) ([]types.BuildRun, string, error) {
		return s.Store.Builds().Runs(ctx, projectKey, name, p)
	})
	if err != nil {
		return nil, fmt.Errorf("build: runs of %s: %w", name, err)
	}
	return out, nil
}

// writeDocument writes run b's document as its item.
func (s *Service) writeDocument(
	ctx context.Context,
	c repo.Config,
	b types.Build,
	by string,
) (types.Item, error) {
	_, path := b.DocumentItem()
	it, err := s.items().Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: bytes.NewReader(b.Document), By: by,
	})
	if err != nil {
		return types.Item{}, fmt.Errorf("build: write document %s: %w", path, err)
	}
	return it, nil
}

// exists reports whether an item is at path in the build-info repository.
// It is asked before a purge rather than ignoring the purge's ErrNotFound,
// because a failed nested transaction rolls the enclosing one back on the
// SQL stores even when its caller drops the error.
func (s *Service) exists(ctx context.Context, c repo.Config, path string) (bool, error) {
	_, err := s.Store.Items().Get(ctx, c.Key, false, path)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("build: get %s: %w", path, err)
	}
	return true, nil
}

// dropDocument removes a deleted run's document item for good, and its
// build name's folder once nothing is left in it. A document already
// deleted through the item API is no error: the run goes all the same.
func (s *Service) dropDocument(ctx context.Context, c repo.Config, r types.BuildRun) error {
	if err := s.purge(ctx, c, documentPath(r)); err != nil {
		return err
	}
	folder := types.EncodeBuildInfoSegment(r.Name)
	ok, err := s.exists(ctx, c, folder)
	if err != nil || !ok {
		return err
	}
	left, _, err := s.Store.Items().Descendants(ctx, c.Key, false, folder, store.Page{Limit: 1})
	if err != nil {
		return fmt.Errorf("build: list %s: %w", folder, err)
	}
	if len(left) > 0 {
		return nil
	}
	return s.purge(ctx, c, folder)
}

func (s *Service) purge(ctx context.Context, c repo.Config, path string) error {
	ok, err := s.exists(ctx, c, path)
	if err != nil || !ok {
		return err
	}
	if _, err := s.items().Purge(ctx, item.Target{Repo: c, Path: path}); err != nil {
		return fmt.Errorf("build: purge %s: %w", path, err)
	}
	return nil
}

// documentPath is the path of a run's document item in its build-info
// repository.
func documentPath(r types.BuildRun) string {
	_, path := types.Build{
		ProjectKey: r.ProjectKey, Name: r.Name, Number: r.Number, Started: r.Started,
	}.DocumentItem()
	return path
}

// collect reads every page of a store listing.
func collect[T any](size int, list func(store.Page) ([]T, string, error)) ([]T, error) {
	var out []T
	page := store.Page{Limit: store.MaxLimit}
	if size > 0 {
		page.Limit = size
	}
	for {
		batch, next, err := list(page)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}
