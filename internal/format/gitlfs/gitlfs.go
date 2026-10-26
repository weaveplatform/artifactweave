// Package gitlfs owns the content-addressed object layout of Git LFS repositories.
package gitlfs

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func Serves(t repo.PackageType) bool { return t == repo.GitLFS }

type Handler struct{ generic.Handler }

// ObjectPath is Artifactory's two-level SHA-256 object layout.
func ObjectPath(oid string) (string, bool) {
	if len(oid) != 64 {
		return "", false
	}
	for _, c := range oid {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return "", false
		}
	}
	return "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid, true
}

func (h Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if w.Op == format.OpChecksum {
		return w, format.Reject(
			http.StatusConflict,
			"Git LFS objects do not accept checksum sidecars",
		)
	}
	oid := w.Path[strings.LastIndexByte(w.Path, '/')+1:]
	path, ok := ObjectPath(oid)
	if !ok || path != w.Path || w.Checksums.SHA256 != oid {
		return w, format.Reject(http.StatusConflict, "invalid Git LFS object or checksum")
	}
	w, err := h.Handler.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("gitlfs: deploy policy: %w", err)
	}
	return w, nil
}

// The object tree is the native index. Delete and restore change the batch
// view directly, so reindex has no derived metadata to rebuild.
func (Handler) Reindex(context.Context, format.Env, repo.Config) error { return nil }
