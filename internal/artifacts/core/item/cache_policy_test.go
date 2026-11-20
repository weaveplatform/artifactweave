package item

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitCachePolicyRequiresBoundAuthority(t *testing.T) {
	for _, mode := range []string{"bound", "zero", "no-fence", "other-repository", "other-target", "other-source", "other-incarnation", "metadata", "metadata-published"} {
		t.Run(mode, func(t *testing.T) {
			w, upload := remoteWorld(t)
			upload.Repo.IncludesPattern = new("files/**")
			policy, err := format.NewCachePolicy(upload.Repo, upload.Token.Source, upload.Token.Key.Path, "files/allowed.bin")
			if err != nil {
				t.Fatal(err)
			}
			upload.CachePolicy = policy
			upload.CheckCurrent = func(context.Context, Store) error { return nil }
			switch mode {
			case "zero":
				upload.CachePolicy = format.CachePolicy{}
			case "no-fence":
				upload.CheckCurrent = nil
			case "other-repository":
				upload.Repo.Key = "other"
			case "other-target":
				upload.Token.Key.Path = "other.bin"
			case "other-source":
				upload.Token.Source.Generation = "other"
			case "other-incarnation":
				upload.Token.Source.Repository = "other-incarnation"
			case "metadata", "metadata-published":
				upload.Kind = model.RemoteMetadata
				upload.PublishMetadata = mode == "metadata-published"
			}
			out, err := w.svc.Cache(t.Context(), upload)
			if mode == "bound" {
				if err != nil || out.Item.FullPath() != "a.bin" || out.Entry.Token.Key.Path != "a.bin" {
					t.Fatal("bound cache publication", out.Item, err)
				}
				return
			}
			if err == nil {
				t.Fatal("unbound admission succeeded")
			}
			if mode != "zero" && !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("wrong binding refusal", err)
			}
			if _, err := w.db.Items().Get(t.Context(), "remote", true, "a.bin"); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("invalid binding published an item", err)
			}
		})
	}
}

func TestUnitCacheCurrentAuthorityPrecedesSourceFence(t *testing.T) {
	for _, revoked := range []bool{true, false} {
		t.Run(map[bool]string{true: "revoked", false: "current"}[revoked], func(t *testing.T) {
			w, upload := remoteWorld(t)
			w.svc.Store = storeFaults{Store: w.db, fence: errDown}
			checked := false
			upload.CheckCurrent = func(ctx context.Context, _ Store) error {
				if !w.db.InTx(ctx) {
					t.Fatal("authority check ran outside publication transaction")
				}
				checked = true
				if revoked {
					return kernel.ErrForbidden
				}
				return nil
			}
			_, err := w.svc.Cache(t.Context(), upload)
			want := errDown
			if revoked {
				want = kernel.ErrForbidden
			}
			if !checked || !errors.Is(err, want) {
				t.Fatalf("authority checked=%v, publication=%v, want %v", checked, err, want)
			}
			if _, err := w.db.RemoteCache().Read(t.Context(), upload.Token.Key); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("refused publication retained cache state: %v", err)
			}
			if _, err := w.db.Items().Get(t.Context(), upload.Repo.Key, true, upload.Token.Key.Path); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("refused publication retained item: %v", err)
			}
		})
	}
}
