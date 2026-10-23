// Package skills indexes the canonical ZIP bundles of local Skills repositories.
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func Serves(t repo.PackageType) bool { return t == repo.Skills }

func ValidSlug(v string) bool { return len(v) <= 128 && slugPattern.MatchString(v) }

func ValidVersion(v string) bool {
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	return len(v) <= 128 && !strings.HasPrefix(v, "v") &&
		strings.Count(core, ".") == 2 && semver.IsValid("v"+v)
}

func Path(slug, version string) string {
	return slug + "/" + version + "/" + slug + "-" + version + ".zip"
}

func ParsePath(p string) (slug, version string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || !ValidSlug(parts[0]) || !ValidVersion(parts[1]) ||
		p != Path(parts[0], parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

type Handler struct{ generic.Handler }

type inspection struct {
	sha  string
	meta Metadata
	err  error
}

func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Checksums.SHA256 == "" || !strings.HasSuffix(w.Path, ".zip") {
		return nil, nil //nolint:nilnil // no ZIP content to inspect
	}
	rc, info, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, fmt.Errorf("skills: open bundle: %w", err)
	}
	defer rc.Close()
	meta, err := ReadArchive(rc, info.Size)
	return &inspection{sha: w.Checksums.SHA256, meta: meta, err: err}, nil
}

func (Handler) Admit(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) (format.Write, error) {
	if w.Op != format.OpChecksum && !w.Cached && strings.HasSuffix(w.Path, ".zip") {
		if _, _, ok := ParsePath(w.Path); !ok {
			return w, format.Reject(http.StatusConflict, "invalid Skills bundle path")
		}
		in, ok := env.Inspection.(*inspection)
		if !ok || in.err != nil {
			return w, format.Reject(http.StatusConflict, "invalid Skills bundle")
		}
	}
	if err := generic.CheckBlackedOut(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // preserve the policy rejection
	}
	if err := generic.CheckPatterns(c, w.Path); err != nil {
		return w, err //nolint:wrapcheck // preserve the policy rejection
	}
	return w, nil
}

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || it.Cached || !strings.HasSuffix(it.FullPath(), ".zip") {
		return nil
	}
	slug, version, ok := ParsePath(it.FullPath())
	if !ok {
		return nil
	}
	meta, err := metadataOf(ctx, env, it)
	if err != nil {
		return err
	}
	meta.Slug, meta.Version, meta.Fingerprint = slug, version, it.Actual.SHA256
	meta, err = setProperties(ctx, env.Store, it, meta)
	if err != nil {
		return err
	}
	return index(ctx, env.Store, c, it, meta)
}

func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error  { return nil }
func (Handler) Deleted(context.Context, format.Env, repo.Config, []types.Item) error { return nil }

func metadataOf(ctx context.Context, env format.Env, it types.Item) (Metadata, error) {
	if in, ok := env.Inspection.(*inspection); ok && in.sha == it.Actual.SHA256 {
		return in.meta, in.err
	}
	rc, info, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return Metadata{}, fmt.Errorf("skills: open stored bundle: %w", err)
	}
	defer rc.Close()
	return ReadArchive(rc, info.Size)
}

func index(ctx context.Context, st store.Store, c repo.Config, it types.Item, meta Metadata) error {
	p, err := st.Packages().Ensure(ctx, types.Package{
		RepoKey: c.Key, Type: c.PackageType, Name: meta.Slug, Created: it.Created,
	})
	if err != nil {
		return fmt.Errorf("skills: package %s: %w", meta.Slug, err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("skills: metadata: %w", err)
	}
	_, err = pkgindex.Sync(ctx, st, p, pkgindex.IndexedVersion{
		Version: meta.Version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
	})
	return err //nolint:wrapcheck // pkgindex names the version
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	var all []pkgindex.Indexed
	bySlug := map[string]int{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		items, next, err := env.Store.Items().Descendants(ctx, c.Key, false, "", page)
		if err != nil {
			return fmt.Errorf("skills: list bundles: %w", err)
		}
		for _, it := range items {
			slug, version, ok := ParsePath(it.FullPath())
			if !ok || it.Folder {
				continue
			}
			meta, err := metadataOf(ctx, env, it)
			if err != nil {
				return fmt.Errorf("skills: inspect %s: %w", it.FullPath(), err)
			}
			meta.Slug, meta.Version, meta.Fingerprint = slug, version, it.Actual.SHA256
			meta, err = setProperties(ctx, env.Store, it, meta)
			if err != nil {
				return err
			}
			data, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("skills: metadata: %w", err)
			}
			i, ok := bySlug[slug]
			if !ok {
				i = len(all)
				bySlug[slug] = i
				all = append(all, pkgindex.Indexed{Name: slug, Created: it.Created})
			}
			all[i].Versions = append(all[i].Versions, pkgindex.IndexedVersion{
				Version: version, Created: it.Created, Metadata: data, Files: []int64{it.ID},
			})
		}
		if next == "" {
			break
		}
		page.After = next
	}
	if err := pkgindex.Reconcile(ctx, env.Store, c, all); err != nil {
		return err //nolint:wrapcheck // pkgindex wraps its own failure
	}
	return nil
}

var (
	_ format.Handler   = Handler{}
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)
