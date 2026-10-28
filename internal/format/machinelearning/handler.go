package machinelearning

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func Serves(t repo.PackageType) bool { return t == repo.MachineLearning }

type (
	Handler        struct{ generic.Handler }
	publicationKey struct{}
)

// Publication is used only inside the native transaction which verifies every
// file before publishing the manifest and rebuilding the index once.
func Publication(ctx context.Context) context.Context {
	return context.WithValue(ctx, publicationKey{}, true)
}

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

func (h Handler) Deleted(ctx context.Context, env format.Env, c repo.Config, _ []types.Item) error {
	if ctx.Value(publicationKey{}) == true {
		return nil
	}
	return h.Reindex(ctx, env, c)
}

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

func (Handler) Classify(_ repo.Config, p string) format.Class {
	if Private(p) {
		return format.ClassMetadata
	}
	return format.ClassUnique
}
