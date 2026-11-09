package gitlfs

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
)

// Serves reports whether t is the Git LFS package type.
func Serves(t repo.PackageType) bool { return t == repo.GitLFS }

// Handler is the Git LFS format's hooks: it admits a write only as an
// object at the path its SHA-256 OID gives, and keeps no index beyond the
// object tree. It embeds the generic hooks, whose deploy policy Admit
// applies after its own checks.
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

// Admit refuses (409) a checksum sidecar upload, which an object never
// takes, and any write that is not of an object at the ObjectPath of its
// file name with that SHA-256. Every other write then meets the generic
// deploy policy, whose refusal it wraps.
func (h Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if w.Op == format.OpChecksum {
		return w, format.Rejectf(
			http.StatusConflict,
			"Git LFS objects do not accept checksum sidecars",
		)
	}
	oid := w.Path[strings.LastIndexByte(w.Path, '/')+1:]
	path, ok := ObjectPath(oid)
	if !ok || path != w.Path || w.Checksums.SHA256 != oid {
		return w, format.Rejectf(http.StatusConflict, "invalid Git LFS object or checksum")
	}
	w, err := h.Handler.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("gitlfs: deploy policy: %w", err)
	}
	return w, nil
}

// Reindex does nothing: the object tree is the native index. Delete and
// restore change the batch view directly, so there is no derived metadata
// to rebuild.
func (Handler) Reindex(context.Context, format.Env, repo.Config) error { return nil }
