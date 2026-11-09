package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Serves reports whether t is the Skills package type.
func Serves(t repo.PackageType) bool { return t == repo.Skills }

// ValidSlug reports whether v is a valid skill slug: at most 128 lower-case
// ASCII letters, digits and hyphens, starting with a letter or digit.
func ValidSlug(v string) bool { return len(v) <= 128 && slugPattern.MatchString(v) }

// ValidVersion reports whether v is a valid skill version: a semantic
// version of at most 128 bytes with exactly three numeric parts and no "v"
// prefix, optionally followed by a pre-release and build metadata.
func ValidVersion(v string) bool {
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	return len(v) <= 128 && !strings.HasPrefix(v, "v") &&
		strings.Count(core, ".") == 2 && semver.IsValid("v"+v)
}

// Path returns where a repository stores the bundle of slug at version:
// <slug>/<version>/<slug>-<version>.zip. It does not check its arguments;
// ParsePath accepts only a path Path builds from a valid slug and version.
func Path(slug, version string) string {
	return slug + "/" + version + "/" + slug + "-" + version + ".zip"
}

// ParsePath returns the slug and version of a bundle path, and ok false
// unless p is exactly the Path of a valid slug and version.
func ParsePath(p string) (slug, version string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || !ValidSlug(parts[0]) || !ValidVersion(parts[1]) ||
		p != Path(parts[0], parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// Handler is the Skills format's hooks: it admits a ZIP only at a
// canonical bundle path and only when it reads as a valid bundle, records a
// stored bundle's skill.* properties, and indexes it as a version of its
// slug. Admit applies the generic deploy policy after its own checks.
type Handler struct{ generic.Handler }

type inspection struct {
	sha  string
	meta Metadata
	err  error
}

// Inspect reads and validates the stored content of a write to a .zip path
// (ReadArchive) before the write's transaction, and returns the result for
// Admit and Deployed, so a bundle is read once per write. A write that
// names no SHA-256 content and any other path inspect nothing. An invalid
// bundle does not fail Inspect, because Admit refuses it; one that cannot
// be opened fails the write.
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

// Admit refuses (409) a write of a .zip that is not at a canonical bundle
// path, or whose content Inspect did not read as a valid bundle; a checksum
// deploy and a write to a remote's cache skip those checks, and other files
// pass them. Every write then meets the generic deploy policy: 404 in a
// blacked-out repository, and 409 for a path the repository's include and
// exclude patterns reject.
func (Handler) Admit(
	ctx context.Context, env format.Env, c repo.Config, w format.Write,
) (format.Write, error) {
	if w.Op != format.OpChecksum && !w.Cached && strings.HasSuffix(w.Path, ".zip") {
		if _, _, ok := ParsePath(w.Path); !ok {
			return w, format.Rejectf(http.StatusConflict, "invalid Skills bundle path")
		}
		in, ok := env.Inspection.(*inspection)
		if !ok || in.err != nil {
			return w, format.Rejectf(http.StatusConflict, "invalid Skills bundle")
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

// Deployed indexes a bundle stored at a canonical bundle path. Its slug and
// version come from the path and its fingerprint is its SHA-256; its other
// metadata comes from SKILL.md, with fields SKILL.md leaves empty taken from
// the item's skill.* properties, such as a publish's display fields, and
// the name defaulting to the slug. It records the skill.* properties,
// changing only those that differ and removing any it no longer derives,
// and syncs the version in the package index under the slug, with that
// metadata and the bundle as its only file. It reuses Inspect's reading when it covers the same content and
// reads the stored bundle otherwise; a bundle that does not read as valid
// fails the write. Folders, a remote's cached files and other paths change
// nothing.
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

// Deleting does nothing: a bundle is its version's only file, and the item
// service prunes a version a delete leaves without files.
func (Handler) Deleting(context.Context, format.Env, repo.Config, types.Item) error { return nil }

// Deleted does nothing: the item service has already pruned the versions
// the delete left without files, and no other file belongs to a version.
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

// Reindex rebuilds the package index from every bundle stored at a
// canonical bundle path: it reads each bundle again, records its skill.*
// properties, groups the versions by slug and reconciles the index. A
// bundle that no longer reads as valid fails the reindex.
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
