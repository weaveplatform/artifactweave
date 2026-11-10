package conan

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Handler keeps the common package index tied to committed Conan manifests.
// Files uploaded before a manifest are invisible to search and resolution;
// the Conan client uploads the manifest last for each revision.
type Handler struct{ generic.Handler }

var errTooManyItems = errors.New("conan: revision listing exceeds limit")

// Admit applies the generic deploy policy (black-out and include and
// exclude patterns) first, keeping its refusal's status, then passes a
// remote's cached write. Any other write must carry the Native mark, or a
// generic item write could create a revision by choosing its path (409),
// and its path must be in the stored layout (400).
func (h Handler) Admit(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) (format.Write, error) {
	w, err := h.Handler.Admit(ctx, env, c, w)
	if err != nil {
		return w, err //nolint:wrapcheck // preserve the generic policy's status
	}
	if w.Cached {
		return w, nil
	}
	if !IsNative(ctx) {
		return w, format.Rejectf(
			http.StatusConflict,
			"Use the Conan client API to publish revisions",
		)
	}
	if _, ok := ParseStored(w.Path); !ok {
		return w, format.Rejectf(http.StatusBadRequest, "Invalid Conan revision path")
	}
	return w, nil
}

// Deployed rebuilds the repository's index with Reindex once a hosted file
// in the layout is stored, since that file may be the manifest that commits
// its revision. Folders, a remote's cached files and paths outside the
// layout change nothing.
func (h Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder || it.Cached {
		return nil
	}
	if _, ok := ParseStored(it.FullPath()); !ok {
		return nil
	}
	return h.Reindex(ctx, env, c)
}

// Deleting does nothing: Deleted rebuilds the index once the items are gone.
func (Handler) Deleting(context.Context, format.Env, repo.Config, model.Item) error { return nil }

// Deleted rebuilds the repository's index with Reindex when any removed
// item was a hosted file in the layout, so a deleted revision leaves the
// index.
func (h Handler) Deleted(
	ctx context.Context, env format.Env, c repo.Config, removed []model.Item,
) error {
	for _, it := range removed {
		if _, ok := ParseStored(it.FullPath()); ok && !it.Cached {
			return h.Reindex(ctx, env, c)
		}
	}
	return nil
}

// Files walks one storage folder with bounded keyset pagination. The caller
// must filter by its principal before returning any name or metadata.
func Files(
	ctx context.Context,
	st format.Store,
	c repo.Config,
	folder string,
) ([]model.Item, error) {
	var out []model.Item
	for page := (kernel.Page{Limit: 1000}); ; {
		items, next, err := st.Items().Descendants(
			ctx, c.Key, c.Class == repo.Remote, folder, page,
		)
		if err != nil {
			return nil, fmt.Errorf("conan: list %s: %w", folder, err)
		}
		out = append(out, items...)
		if len(out) > 50000 {
			return nil, errTooManyItems
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

// Reindex rebuilds repository c's package index from its files (a remote's
// from its cache): a package per name@user/channel, a version per recipe
// version, and as each version's files those of its committed recipe
// revisions and of their committed package revisions. A version and a
// package take their oldest file's creation time. It fails rather than
// index part of a repository that holds more than 50,000 items.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	items, err := Files(ctx, env.Store, c, "")
	if err != nil {
		return err
	}
	type version struct {
		created time.Time
		files   []int64
	}
	byName := map[string]map[string]*version{}
	complete := map[string]bool{}
	for _, it := range items {
		f, ok := ParseStored(it.FullPath())
		if !ok || it.Folder {
			continue
		}
		if f.File == Manifest {
			complete[f.Root()] = true
		}
	}
	for _, it := range items {
		f, ok := ParseStored(it.FullPath())
		if !ok || it.Folder || !complete[f.Ref.RecipeRoot()] ||
			f.Package && !complete[f.Root()] {
			continue
		}
		name := f.Ref.Name + "@" + f.Ref.User + "/" + f.Ref.Channel
		if byName[name] == nil {
			byName[name] = map[string]*version{}
		}
		v := byName[name][f.Ref.Version]
		if v == nil {
			v = &version{}
			byName[name][f.Ref.Version] = v
		}
		v.files = append(v.files, it.ID)
		if v.created.IsZero() || it.Created.Before(v.created) {
			v.created = it.Created
		}
	}
	want := make([]pkgindex.Indexed, 0, len(byName))
	for name, versions := range byName {
		p := pkgindex.Indexed{Name: name}
		for value, v := range versions {
			p.Versions = append(p.Versions, pkgindex.IndexedVersion{
				Version: value, Created: v.created, Files: v.files,
			})
			if p.Created.IsZero() || v.created.Before(p.Created) {
				p.Created = v.created
			}
		}
		slices.SortFunc(p.Versions, func(a, b pkgindex.IndexedVersion) int {
			return strings.Compare(a.Version, b.Version)
		})
		want = append(want, p)
	}
	return pkgindex.Reconcile(ctx, env.Store, c, want) //nolint:wrapcheck // includes index context
}

var (
	_ format.Handler   = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)
