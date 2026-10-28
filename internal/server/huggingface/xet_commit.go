package huggingface

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/auth"
	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func xetStagePath(id format.Identity, subject, sha string) string {
	sum := sha256.Sum256([]byte(subject + "\n" + sha))
	return xetPath(id, "stages", fmt.Sprintf("%x", sum))
}

// A raw blob digest is not authority to publish private CAS bytes through the
// Hub commit API. A native verified upload records the caller/model grant;
// committed readable snapshots may also be reused within that model.
func (h handler) authorizeXetCommit(r *http.Request, path, sha string) error {
	id, _, _, ok := format.ParseSnapshotPath(path)
	if !ok {
		return badIdentity()
	}
	p, _ := auth.FromContext(r.Context())
	raw, err := h.readXet(r, xetStagePath(id, p.Name, sha), 64)
	if err == nil && string(raw) == sha {
		return nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	page := store.Page{Limit: store.MaxLimit}
	readable := false
	key := pkgroute.RequestFrom(r).Repo.Key
	for {
		items, next, err := h.k.DB().
			Items().
			FindByChecksum(r.Context(), types.Checksums{SHA256: sha}, page)
		if err != nil {
			return fmt.Errorf("huggingface: authorize Xet content reuse: %w", err)
		}
		for _, it := range items {
			if format.IsXetPath(it.FullPath()) {
				continue
			}
			existing, _, _, valid := format.ParseSnapshotPath(it.FullPath())
			if valid && it.RepoKey == key && existing == id &&
				h.k.Can(r, it.FullPath(), types.ActionRead) {
				readable = true
			}
		}
		if next == "" {
			break
		}
		page.After = next
	}
	if readable {
		return nil
	}
	return apierror.New(
		http.StatusForbidden,
		"content requires a verified upload for this model and caller, or a readable model snapshot",
	)
}
