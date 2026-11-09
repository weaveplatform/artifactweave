package machinelearning

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Serves reports whether t is the Machine Learning package type.
func Serves(t repo.PackageType) bool { return t == repo.MachineLearning }

type (
	// Handler is the Machine Learning format's hooks: a model version is
	// written only through a FrogML transaction, private records are never
	// served, copied or moved, and only complete, verified versions are
	// published. It embeds the generic hooks, whose deploy policy Admit
	// applies first.
	Handler struct{ generic.Handler }

	publicationKey struct{}
)

// Publication is used only inside the native transaction which verifies every
// file before publishing the manifest and rebuilding the index once.
func Publication(ctx context.Context) context.Context {
	return context.WithValue(ctx, publicationKey{}, true)
}

// Admit applies the generic deploy policy, whose refusal it wraps, and then
// refuses (400) a path outside the model layout (ParsePath), a cache write
// and a file over MaxFileSize. Writes inside the native transaction
// (Publication) pass. Any other write must deploy a file into a staging
// folder (409 otherwise, so no published file or transaction record is
// written directly) whose transaction is live and the writer's own
// (CheckTransaction: 403 for another principal, 409 once it has expired).
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := (generic.Handler{}).Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("machinelearning: admission: %w", err)
	}
	id, tx, rest, ok := ParsePath(w.Path)
	if !ok || w.Cached || w.Size > MaxFileSize {
		return w, invalid("invalid machine learning artifact path or size")
	}
	if ctx.Value(publicationKey{}) == true {
		return w, nil
	}
	if tx == "" || rest == TransactionName || w.Op != format.OpDeploy {
		return w, format.Rejectf(
			http.StatusConflict,
			"model versions must be published through a FrogML transaction",
		)
	}
	if err := CheckTransaction(ctx, env, c, id, tx, w.By); err != nil {
		return w, err
	}
	return w, nil
}

// CheckTransaction rechecks ownership and expiry on every upload and commit.
// Knowing the millisecond transaction ID is not an authorization capability.
func CheckTransaction(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	id Identity,
	tx string, by string,
) error {
	it, err := env.Store.Items().Get(ctx, c.Key, false, id.Staging(tx)+"/"+TransactionName)
	if err != nil {
		return fmt.Errorf("machinelearning: transaction: %w", err)
	}
	if by == "" || by == "anonymous" || by != it.CreatedBy || it.Folder {
		return format.Rejectf(
			http.StatusForbidden,
			"model upload transaction belongs to another principal",
		)
	}
	now := time.Now()
	if env.Now != nil {
		now = env.Now()
	}
	if now.Sub(it.Created) > 24*time.Hour {
		return format.Rejectf(http.StatusConflict, "model upload transaction expired")
	}
	return nil
}

// Deployed lets folders, writes inside the native transaction and staging
// files pass, and refuses (400) any other path outside the model layout. A
// published file stored again, as by a restore, must be one its version's
// manifest declares, with that size and SHA-256, whenever the manifest is
// stored (409). The index is then rebuilt (Reindex), so a version made
// complete again is published again.
func (h Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || ctx.Value(publicationKey{}) == true {
		return nil
	}
	id, tx, rest, ok := ParsePath(it.FullPath())
	if !ok {
		return invalid("invalid restored model path")
	}
	if tx != "" {
		return nil
	}
	m, _, err := Read(ctx, env, c, id)
	if err != nil && !Missing(err) {
		return err
	}
	if err == nil {
		if rest != ManifestName {
			found := false
			for _, file := range m.Files() {
				if file.Path == rest && file.Artifact.Checksums.SHA256 == it.Actual.SHA256 &&
					file.Artifact.Size == it.Size {
					found = true
				}
			}
			if !found {
				return format.Rejectf(
					http.StatusConflict,
					"restored file differs from the model manifest",
				)
			}
		}
	}
	return h.Reindex(ctx, env, c)
}

// Deleted rebuilds the index (Reindex) after any delete outside the native
// transaction, so removing a declared file withdraws its version at once.
func (h Handler) Deleted(ctx context.Context, env format.Env, c repo.Config, _ []types.Item) error {
	if ctx.Value(publicationKey{}) == true {
		return nil
	}
	return h.Reindex(ctx, env, c)
}

// GuardTransferSource refuses (403) to copy or move a private record (a
// staging file or a manifest, Private), and lets a published file go only
// while its version's manifest is stored and every declared file verifies
// (Verify).
func (Handler) GuardTransferSource(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
) error {
	if Private(it.FullPath()) {
		return format.Rejectf(
			http.StatusForbidden,
			"private model records cannot be copied or moved",
		)
	}
	id, _, _, ok := ParsePath(it.FullPath())
	if !ok {
		return invalid("invalid model artifact path")
	}
	m, _, err := Read(ctx, env, c, id)
	if err != nil {
		return err
	}
	_, err = Verify(ctx, env, c, id.Prefix(), m)
	return err
}

// Resolve serves a model file only from a complete published version:
// staging files, the manifest and any path its version's manifest does not
// declare answer 404, and a download also fails while a declared file is
// missing or differs (Verify). The manifest GET is the manifest's only
// reader.
func (Handler) Resolve(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	d format.Download,
) (string, error) {
	id, tx, rest, ok := ParsePath(d.Path)
	if !ok || tx != "" || rest == ManifestName {
		return "", format.Rejectf(http.StatusNotFound, "model artifact not found")
	}
	m, _, err := Read(ctx, env, c, id)
	if err != nil {
		return "", err
	}
	files, err := Verify(ctx, env, c, id.Prefix(), m)
	if err != nil {
		return "", err
	}
	for _, it := range files {
		if it.FullPath() == d.Path {
			return d.Path, nil
		}
	}
	return "", format.Rejectf(http.StatusNotFound, "model artifact not found")
}

// Reindex publishes only complete manifest-backed versions. Removing one file
// withdraws the version; restoring the full set makes it visible again.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	err := env.Store.Tx(ctx, func(ctx context.Context) error {
		groups := map[string]*pkgindex.Indexed{}
		page := store.Page{Limit: store.MaxLimit}
		for {
			items, next, err := env.Store.Items().Descendants(ctx, c.Key, false, "models", page)
			if err != nil {
				return fmt.Errorf("machinelearning: list manifests: %w", err)
			}
			for _, it := range items {
				id, tx, rest, ok := ParsePath(it.FullPath())
				if !ok || it.Folder || tx != "" || rest != ManifestName {
					continue
				}
				m, manifest, err := Read(ctx, env, c, id)
				if err != nil {
					return err
				}
				files, err := Verify(ctx, env, c, id.Prefix(), m)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				g := groups[id.Name]
				if g == nil {
					g = &pkgindex.Indexed{Name: id.Name, Created: manifest.Created}
					groups[id.Name] = g
				}
				ids := []int64{manifest.ID}
				for _, file := range files {
					ids = append(ids, file.ID)
				}
				g.Versions = append(
					g.Versions,
					pkgindex.IndexedVersion{
						Version: id.Version,
						Created: manifest.Created,
						Files:   ids,
					},
				)
			}
			if next == "" {
				break
			}
			page.After = next
		}
		wanted := make([]pkgindex.Indexed, 0, len(groups))
		for _, g := range groups {
			slices.SortFunc(
				g.Versions,
				func(a, b pkgindex.IndexedVersion) int { return strings.Compare(a.Version, b.Version) },
			)
			wanted = append(wanted, *g)
		}
		slices.SortFunc(
			wanted,
			func(a, b pkgindex.Indexed) int { return strings.Compare(a.Name, b.Name) },
		)
		if env.CheckReindex != nil {
			if err := env.CheckReindex(ctx); err != nil {
				return fmt.Errorf("machinelearning: reindex authorization: %w", err)
			}
		}
		if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
			return fmt.Errorf("machinelearning: reconcile: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("machinelearning: reindex: %w", err)
	}
	return nil
}

// Classify marks a private record (a staging file or a manifest, Private) as
// metadata, and every published model file as unique content.
func (Handler) Classify(_ repo.Config, p string) format.Class {
	if Private(p) {
		return format.ClassMetadata
	}
	return format.ClassUnique
}
