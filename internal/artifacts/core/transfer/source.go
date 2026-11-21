package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/antpath"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// Store keeps source snapshots in the same unit of work as local checkpoints.
// InTx prevents a content open from accidentally carrying a metadata transaction
// into blob/network I/O. Properties supplies the existing per-item read guard.
type Store interface {
	tx.Runner
	InTx(context.Context) bool
	Repos() port.RepoStore
	Items() port.ItemStore
	RemoteCache() port.RemoteCacheStore
}

// Source owns no replication configuration or transport. Guard and Current are
// bound by the artifacts module to current project and credential authority.
type Source struct {
	Store   Store
	Blobs   blob.Store
	Guard   func(context.Context) error
	Current func(context.Context) (accessapi.Decider, error)
}

func (s Source) run(ctx context.Context, fn func(context.Context, accessapi.Decider) error) error {
	if s.Store == nil || s.Guard == nil || s.Current == nil {
		return fmt.Errorf("content source authority is unavailable: %w", kernel.ErrStore)
	}
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Guard(ctx); err != nil {
			return fmt.Errorf("guard content source: %w", err)
		}
		decider, err := s.Current(ctx)
		if err != nil {
			return fmt.Errorf("current content source authority: %w", err)
		}
		if decider == nil {
			return kernel.ErrForbidden
		}
		return fn(ctx, decider)
	})
	if err != nil {
		return fmt.Errorf("content source: %w", err)
	}
	return nil
}

func (s Source) repository(ctx context.Context, key string, decider accessapi.Decider) (api.TransferRepository, error) {
	c, err := s.Store.Repos().ProjectRepository(ctx, key)
	if err != nil {
		return api.TransferRepository{}, fmt.Errorf("read content repository: %w", err)
	}
	resource := accessapi.Repo{Key: key, Class: string(c.Class)}
	if !decider.AllowsFolder(accessapi.ResourceArtifact, resource, "", accessapi.ActionRead) {
		return api.TransferRepository{}, kernel.ErrForbidden
	}
	if c.Class != repo.Local {
		return api.TransferRepository{}, fmt.Errorf("content source requires a local repository: %w", kernel.ErrInvalid)
	}
	description, err := s.Store.Repos().EventContext(ctx, key)
	if err != nil {
		return api.TransferRepository{}, fmt.Errorf("read content repository type: %w", err)
	}
	identity, err := s.Store.RemoteCache().Source(ctx, key)
	if err != nil {
		return api.TransferRepository{}, fmt.Errorf("read content repository lifetime: %w", err)
	}
	return api.TransferRepository{
		Key: key, Incarnation: string(identity.Repository), Generation: string(identity.Generation), PackageType: description.PackageType,
	}, nil
}

// Repository returns only descriptive identity after current Read authority.
func (s Source) Repository(ctx context.Context, key string) (api.TransferRepository, error) {
	var out api.TransferRepository
	err := s.run(ctx, func(ctx context.Context, decider accessapi.Decider) error {
		var err error
		out, err = s.repository(ctx, key, decider)
		return err
	})
	if err != nil {
		return api.TransferRepository{}, err
	}
	return out, nil
}

func (s Source) currentRepository(ctx context.Context, expected api.TransferRepository, decider accessapi.Decider) error {
	actual, err := s.repository(ctx, expected.Key, decider)
	if err != nil {
		return err
	}
	if expected.Incarnation == "" || expected.Generation == "" || actual != expected {
		return kernel.ErrConflict
	}
	return nil
}

type selection struct {
	root    string
	include antpath.Pattern
	exclude *antpath.Pattern
}

func selectPaths(in api.TransferSelection) (selection, error) {
	for _, value := range []string{in.Root, in.Include, in.Exclude} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return selection{}, kernel.ErrInvalid
		}
	}
	root := strings.Trim(in.Root, "/")
	if root == "." {
		root = ""
	}
	if root != "" && !canonicalPath(root) {
		return selection{}, kernel.ErrInvalid
	}
	out := selection{root: root, include: antpath.CompilePattern(in.Include)}
	if in.Exclude != "" {
		pattern := antpath.CompilePattern(in.Exclude)
		out.exclude = &pattern
	}
	return out, nil
}

func canonicalPath(path string) bool {
	if path == "" || !utf8.ValidString(path) || path != strings.Trim(path, "/") {
		return false
	}
	_, _, _, err := kernel.SplitItemPath(path)
	return err == nil
}

func (s selection) matches(path string) bool {
	return canonicalPath(path) && (s.root == "" || path == s.root || strings.HasPrefix(path, s.root+"/")) &&
		s.include.Match(path) && (s.exclude == nil || !s.exclude.Match(path))
}

// Items returns one bounded canonical page, filtering before disclosure. A
// selected but unreadable path fails the whole page; it never becomes a miss.
func (s Source) Items(ctx context.Context, repository api.TransferRepository, query api.TransferSelection, page kernel.Page) ([]api.TransferItem, string, error) {
	scope, err := selectPaths(query)
	if err != nil {
		return nil, "", fmt.Errorf("content selection: %w", err)
	}
	page, err = page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("content page: %w", err)
	}
	if _, err := page.Key(1); err != nil {
		return nil, "", fmt.Errorf("content continuation: %w", err)
	}
	var out []api.TransferItem
	var next string
	err = s.run(ctx, func(ctx context.Context, decider accessapi.Decider) error {
		if err := s.currentRepository(ctx, repository, decider); err != nil {
			return err
		}
		var rows []model.Item
		if scope.root != "" {
			it, err := s.Store.Items().Get(ctx, repository.Key, false, scope.root)
			if err != nil && !errors.Is(err, kernel.ErrNotFound) {
				return fmt.Errorf("read exact content root: %w", err)
			}
			if err == nil && !it.Folder {
				if page.After == "" {
					rows = []model.Item{it}
				}
				return s.project(ctx, decider, repository, query, scope, rows, &out)
			}
		}
		var err error
		rows, next, err = s.Store.Items().DescendantsByFullPath(ctx, repository.Key, false, scope.root, page)
		if err != nil {
			return fmt.Errorf("page source content: %w", err)
		}
		return s.project(ctx, decider, repository, query, scope, rows, &out)
	})
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func (s Source) project(
	ctx context.Context,
	decider accessapi.Decider,
	repository api.TransferRepository,
	query api.TransferSelection,
	scope selection,
	rows []model.Item,
	out *[]api.TransferItem,
) error {
	for _, it := range rows {
		if !scope.matches(it.FullPath()) {
			continue
		}
		if !decider.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: repository.Key, Class: string(repo.Local)}, it.FullPath(), accessapi.ActionRead) {
			return kernel.ErrForbidden
		}
		current, err := s.snapshot(ctx, repository, it, query.Properties, query.Statistics)
		if err != nil {
			return err
		}
		*out = append(*out, current)
	}
	return nil
}

func (s Source) snapshot(ctx context.Context, repository api.TransferRepository, candidate model.Item, properties, statistics bool) (api.TransferItem, error) {
	// Lock before rereading the item, not after: properties, statistics and
	// content can otherwise describe different versions under READ COMMITTED.
	props, err := s.Store.Items().Properties(ctx, candidate.ID)
	if err != nil {
		return api.TransferItem{}, fmt.Errorf("guard source item: %w", err)
	}
	it, err := s.Store.Items().GetByID(ctx, candidate.ID)
	if err != nil {
		return api.TransferItem{}, fmt.Errorf("refresh source item: %w", err)
	}
	if it.RepoKey != repository.Key || it.Cached || it.FullPath() != candidate.FullPath() {
		return api.TransferItem{}, kernel.ErrConflict
	}
	out := api.TransferItem{
		Repository: repository, ID: it.ID, Path: it.FullPath(), Folder: it.Folder, Size: it.Size, Actual: it.Actual,
		Created: it.Created.UTC(), Modified: it.Modified.UTC(), Updated: it.Updated.UTC(),
		PropertiesModified: it.PropertiesModified.UTC(), PropertiesIncluded: properties,
	}
	if properties {
		out.Properties = maps.Clone(props)
		for key, values := range out.Properties {
			out.Properties[key] = slices.Clone(values)
		}
	}
	if statistics && !it.Folder {
		stats, err := s.Store.Items().Stats(ctx, it.ID)
		if err != nil {
			return api.TransferItem{}, fmt.Errorf("read source item statistics: %w", err)
		}
		out.Statistics = &api.TransferStatistics{
			DownloadCount: stats.DownloadCount, LastDownloaded: stats.LastDownloaded.UTC(), LastDownloadedBy: stats.LastDownloadedBy,
		}
	}
	out.Version, err = version(out)
	if err != nil {
		return api.TransferItem{}, err
	}
	return out, nil
}

func version(item api.TransferItem) (string, error) {
	item.Version = ""
	data, err := json.Marshal(item)
	if err != nil {
		return "", fmt.Errorf("source snapshot encoding: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// WithCurrent fences local success against current source identity and facts.
func (s Source) WithCurrent(ctx context.Context, expected api.TransferItem, fn func(context.Context) error) error {
	fingerprint, err := version(expected)
	if err != nil {
		return err
	}
	if expected.ID <= 0 || !canonicalPath(expected.Path) || fingerprint != expected.Version {
		return kernel.ErrInvalid
	}
	return s.run(ctx, func(ctx context.Context, decider accessapi.Decider) error {
		if err := s.currentRepository(ctx, expected.Repository, decider); err != nil {
			return err
		}
		if !decider.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: expected.Repository.Key, Class: string(repo.Local)}, expected.Path, accessapi.ActionRead) {
			return kernel.ErrForbidden
		}
		it, err := s.Store.Items().Get(ctx, expected.Repository.Key, false, expected.Path)
		if err != nil {
			return fmt.Errorf("read current source item: %w", err)
		}
		actual, err := s.snapshot(ctx, expected.Repository, it, expected.PropertiesIncluded, expected.Statistics != nil)
		if err != nil {
			return err
		}
		if actual.Version != expected.Version {
			return kernel.ErrConflict
		}
		if fn != nil {
			return fn(ctx)
		}
		return nil
	})
}

// Open never holds a metadata transaction while a blob backend opens content.
func (s Source) Open(ctx context.Context, expected api.TransferItem) (io.ReadSeekCloser, error) {
	if s.Store == nil || s.Blobs == nil {
		return nil, fmt.Errorf("content source storage is unavailable: %w", kernel.ErrStore)
	}
	if s.Store.InTx(ctx) || expected.Folder {
		return nil, fmt.Errorf("content cannot be opened in this context: %w", kernel.ErrInvalid)
	}
	if err := s.WithCurrent(ctx, expected, nil); err != nil {
		return nil, err
	}
	body, info, err := s.Blobs.Open(ctx, expected.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("open immutable source content: %w", err)
	}
	if info.Size != expected.Size || info.Digests.SHA256 != expected.Actual.SHA256 {
		return nil, fmt.Errorf("source blob does not match item: %w", errors.Join(kernel.ErrConflict, body.Close()))
	}
	return body, nil
}

// WithAbsent permits only an authorized exact-path absence observation.
func (s Source) WithAbsent(ctx context.Context, repository api.TransferRepository, path string, fn func(context.Context) error) error {
	if !canonicalPath(path) {
		return kernel.ErrInvalid
	}
	return s.run(ctx, func(ctx context.Context, decider accessapi.Decider) error {
		if err := s.currentRepository(ctx, repository, decider); err != nil {
			return err
		}
		if !decider.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: repository.Key, Class: string(repo.Local)}, path, accessapi.ActionRead) {
			return kernel.ErrForbidden
		}
		_, err := s.Store.Items().Get(ctx, repository.Key, false, path)
		if err == nil {
			return kernel.ErrConflict
		}
		if !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("check exact source absence: %w", err)
		}
		if fn != nil {
			return fn(ctx)
		}
		return nil
	})
}

var _ api.TransferSource = Source{}
