package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func runVirtualCopies(t *testing.T, newStore Factory) {
	t.Run("VirtualCopies", func(t *testing.T) {
		s := newStore(t)
		ctx := t.Context()
		for _, v := range []repo.Config{{Key: "v", Class: repo.Virtual, PackageType: repo.Maven}, {Key: "local", Class: repo.Local, PackageType: repo.Maven}} {
			if err := s.Repos().Create(ctx, v); err != nil {
				t.Fatal(err)
			}
		}
		c := virtual.Copy{
			Virtual:      "v",
			Path:         "a.pom",
			Member:       "local",
			SourceSHA256: strings.Repeat("1", 64),
			Policy:       "discard_active_reference",
			Item:         model.Item{Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Size: 10, Created: time.Now().UTC().Truncate(time.Microsecond)},
		}
		if _, err := s.VirtualCopies().Get(ctx, "v", "missing"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal(err)
		}
		for range 2 {
			if err := s.VirtualCopies().Put(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.VirtualCopies().Get(ctx, "v", c.Path)
		if err != nil || got != c {
			t.Fatalf("%+v %v", got, err)
		}
		b, err := s.Blobs().Get(ctx, c.Item.Actual.SHA256)
		if err != nil || b.Refs != 1 {
			t.Fatalf("refs %+v %v", b, err)
		}
		replacement := c
		replacement.Item.Actual.SHA256 = strings.Repeat("b", 64)
		if err = s.Tx(ctx, func(ctx context.Context) error {
			if putErr := s.VirtualCopies().Put(ctx, replacement); putErr != nil {
				return fmt.Errorf("put rollback copy: %w", putErr)
			}
			return kernel.ErrInvalid
		}); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal(err)
		}
		got, err = s.VirtualCopies().Get(ctx, "v", c.Path)
		if err != nil || got.Item.Actual != c.Item.Actual {
			t.Fatal("rollback", got, err)
		}
		if err = s.VirtualCopies().Put(ctx, replacement); err != nil {
			t.Fatal(err)
		}
		b, err = s.Blobs().Get(ctx, c.Item.Actual.SHA256)
		if err != nil || b.Refs != 0 {
			t.Fatalf("released %+v %v", b, err)
		}
		if err = s.Repos().Delete(ctx, "v"); err != nil {
			t.Fatal(err)
		}
		b, err = s.Blobs().Get(ctx, replacement.Item.Actual.SHA256)
		if err != nil || b.Refs != 0 {
			t.Fatalf("deleted %+v %v", b, err)
		}
		if _, err = s.VirtualCopies().Get(ctx, "v", c.Path); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("copy survived repository deletion", err)
		}
		if err = s.VirtualCopies().Put(ctx, c); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("missing repository", err)
		}
		c.Virtual = "local"
		if err = s.VirtualCopies().Put(ctx, c); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("not virtual", err)
		}
		if err = s.VirtualCopies().Put(ctx, virtual.Copy{}); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid", err)
		}
	})
}
