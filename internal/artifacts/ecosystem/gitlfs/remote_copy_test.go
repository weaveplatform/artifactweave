package gitlfs

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitRemoteCopyOutsideTheLayoutIsAPlainFile (#1575): a remote's
// cached copy at a path outside the Git LFS object layout is admitted as it is,
// without reading its content (the environment has no blob store), while
// a deploy of the same file to a local repository keeps its refusal.
func TestUnitRemoteCopyOutsideTheLayoutIsAPlainFile(t *testing.T) {
	h := Handler{}
	remote := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.GitLFS})
	local := *repo.Defaults(&repo.Config{Key: "local", Class: repo.Local, PackageType: repo.GitLFS})
	for _, p := range []string{"pkg/file.bin", "objects/00/00/" + strings.Repeat("a", 64) + ""} {
		w := format.Write{
			Op: format.OpDeploy, Cached: true, Path: p,
			Checksums: kernel.Checksums{SHA256: strings.Repeat("a", 64)},
		}
		got, err := h.Admit(t.Context(), format.Env{}, remote, w)
		if err != nil || got.Path != p {
			t.Fatalf("%s: admitted %q, %v", p, got.Path, err)
		}
		w.Cached = false
		var refused *format.RejectionError
		if _, err := h.Admit(t.Context(), format.Env{}, local, w); !errors.As(err, &refused) ||
			refused.Status != http.StatusConflict {
			t.Fatalf("%s: local deploy %v", p, err)
		}
	}
}

// TestUnitRemoteCopyOfAnObjectKeepsItsSHA256 (#1575): a remote's cached
// copy at an object's path must still carry that object's SHA-256.
func TestUnitRemoteCopyOfAnObjectKeepsItsSHA256(t *testing.T) {
	remote := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.GitLFS})
	oid := strings.Repeat("a", 64)
	path, _ := ObjectPath(oid)
	var refused *format.RejectionError
	_, err := Handler{}.Admit(t.Context(), format.Env{}, remote, format.Write{
		Op: format.OpDeploy, Cached: true, Path: path,
		Checksums: kernel.Checksums{SHA256: strings.Repeat("b", 64)},
	})
	if !errors.As(err, &refused) || refused.Status != http.StatusConflict {
		t.Fatalf("mismatched object admitted: %v", err)
	}
}
