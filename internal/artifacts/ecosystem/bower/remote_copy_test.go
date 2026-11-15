package bower

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitRemoteCopyOutsideTheLayoutIsAPlainFile (#1575): a remote's
// cached copy at a path outside the Bower layout is admitted as it is,
// without reading its content (the environment has no blob store), while
// a deploy of the same file to a local repository keeps its refusal.
func TestUnitRemoteCopyOutsideTheLayoutIsAPlainFile(t *testing.T) {
	h := Handler{}
	remote := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Bower})
	local := *repo.Defaults(&repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Bower})
	for _, p := range []string{"pkg/file.bin", "docs/bower.json"} {
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

// TestUnitRemoteCopyOfAManifestOutsideATreeIndexesNothing (#1575): a
// bower.json a remote keeps outside every package tree is a plain file,
// which Deployed does not read (the environment has no blob store).
func TestUnitRemoteCopyOfAManifestOutsideATreeIndexesNothing(t *testing.T) {
	remote := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Bower})
	it := model.Item{RepoKey: "remote", Cached: true, Path: "docs", Name: "bower.json"}
	if err := (Handler{}).Deployed(t.Context(), format.Env{}, remote, it); err != nil {
		t.Fatal(err)
	}
}
