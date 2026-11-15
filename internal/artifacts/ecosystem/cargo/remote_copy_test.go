package cargo

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitRemoteCopyOutsideTheLayoutIsAPlainFile (#1575): a remote's
// cached copy at a path outside the Cargo layout is admitted as it is,
// without reading its content (the environment has no blob store), while
// a deploy of the same file to a local repository keeps its refusal.
func TestUnitRemoteCopyOutsideTheLayoutIsAPlainFile(t *testing.T) {
	h := Handler{}
	remote := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Cargo})
	local := *repo.Defaults(&repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Cargo})
	for _, p := range []string{"pkg/file.bin", "docs/notes.crate"} {
		w := format.Write{
			Op: format.OpDeploy, Cached: true, Path: p,
			Checksums: kernel.Checksums{SHA256: "unread"},
		}
		if _, err := h.Inspect(t.Context(), format.Env{}, remote, w); err != nil {
			t.Fatalf("%s: inspected %v", p, err)
		}
		got, err := h.Admit(t.Context(), format.Env{}, remote, w)
		if err != nil || got.Path != p {
			t.Fatalf("%s: admitted %q, %v", p, got.Path, err)
		}
		w.Cached = false
		var refused *format.RejectionError
		if _, err := h.Admit(t.Context(), format.Env{}, local, w); !errors.As(err, &refused) ||
			refused.Status != http.StatusBadRequest {
			t.Fatalf("%s: local deploy %v", p, err)
		}
	}
}
