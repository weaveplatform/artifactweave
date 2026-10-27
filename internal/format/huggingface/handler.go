package huggingface

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func Serves(t repo.PackageType) bool { return t == repo.HuggingFaceML }

type Handler struct{ generic.Handler }

type deferredReindexKey struct{}

// DeferReindex lets a native multi-file commit publish its snapshot before
// rebuilding the package index once inside the same transaction.
func DeferReindex(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferredReindexKey{}, true)
}

func (Handler) Admit(
	_ context.Context,
	_ format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if w.Op != format.OpChecksum && !w.Cached {
		if _, _, _, ok := ParseSnapshotPath(w.Path); !ok {
			if _, _, ref := ParseRefPath(w.Path); !ref {
				return w, format.Rejectf(http.StatusConflict, "invalid Hugging Face snapshot path")
			}
		}
	}
	if err := generic.CheckBlackedOut(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // preserve policy rejection
	}
	if err := generic.CheckPatterns(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // preserve policy rejection
	}
	return w, nil
}

func (h Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder {
		return nil
	}
	if _, _, _, ok := ParseSnapshotPath(it.FullPath()); !ok {
		return nil
	}
	if ctx.Value(deferredReindexKey{}) == true {
		return nil
	}
	return h.Reindex(ctx, env, c)
}

func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

func (h Handler) Deleted(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	removed []types.Item,
) error {
	for _, it := range removed {
		if _, _, _, ok := ParseSnapshotPath(it.FullPath()); ok {
			return h.Reindex(ctx, env, c)
		}
	}
	return nil
}

type snapshotGroup struct {
	files []types.Item
}

// Reindex reconstructs the native package/version view from committed
// snapshot files. A delete, restore or move cannot leave a stale revision.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	err := env.Store.Tx(ctx, func(ctx context.Context) error {
		groups := map[string]map[string]*snapshotGroup{}
		page := store.Page{Limit: store.MaxLimit}
		cached := pkgindex.Stored(c).Cached
		for {
			items, next, err := env.Store.Items().Descendants(ctx, c.Key, cached, "", page)
			if err != nil {
				return fmt.Errorf("huggingface: list snapshots: %w", err)
			}
			for _, it := range items {
				if it.Folder {
					continue
				}
				id, revision, _, ok := ParseSnapshotPath(it.FullPath())
				if !ok {
					continue
				}
				name := id.Kind + "/" + id.RepoID()
				if groups[name] == nil {
					groups[name] = map[string]*snapshotGroup{}
				}
				if groups[name][revision] == nil {
					groups[name][revision] = &snapshotGroup{}
				}
				groups[name][revision].files = append(groups[name][revision].files, it)
			}
			if next == "" {
				break
			}
			page.After = next
		}
		wanted := make([]pkgindex.Indexed, 0, len(groups))
		for name, versions := range groups {
			entry := pkgindex.Indexed{Name: name}
			for revision, group := range versions {
				ids := make([]int64, 0, len(group.files))
				paths := make([]string, 0, len(group.files))
				v := pkgindex.IndexedVersion{Version: revision}
				for _, it := range group.files {
					ids = append(ids, it.ID)
					paths = append(paths, it.FullPath())
					if v.Created.IsZero() || it.Created.Before(v.Created) {
						v.Created = it.Created
					}
				}
				slices.Sort(paths)
				v.Files = ids
				v.Metadata, _ = json.Marshal(map[string]any{"revision": revision, "files": paths})
				entry.Versions = append(entry.Versions, v)
				if entry.Created.IsZero() || v.Created.Before(entry.Created) {
					entry.Created = v.Created
				}
			}
			slices.SortFunc(entry.Versions, func(a, b pkgindex.IndexedVersion) int {
				return strings.Compare(a.Version, b.Version)
			})
			wanted = append(wanted, entry)
		}
		slices.SortFunc(wanted, func(a, b pkgindex.Indexed) int {
			return strings.Compare(a.Name, b.Name)
		})
		if env.CheckReindex != nil {
			if err := env.CheckReindex(ctx); err != nil {
				return err //nolint:wrapcheck // preserve caller/source fence
			}
		}
		if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
			return fmt.Errorf("huggingface: reconcile index: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("huggingface: reindex transaction: %w", err)
	}
	return nil
}

func (Handler) Classify(_ repo.Config, path string) format.Class {
	if _, _, _, ok := ParseSnapshotPath(path); ok {
		return format.ClassUnique
	}
	return format.ClassMetadata
}

var (
	_ format.Handler    = Handler{}
	_ format.Admitter   = Handler{}
	_ format.Reindexer  = Handler{}
	_ format.Classifier = Handler{}
)
