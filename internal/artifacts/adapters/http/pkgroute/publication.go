package pkgroute

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// Tx refreshes the original caller before a protocol takes metadata locks and
// retains that authority through its transaction. Streaming and network calls
// belong outside fn. fn receives the current caller and decider in its context.
func (k *Kit) Tx(ctx context.Context, fn func(context.Context) error) error {
	err := k.o.Store.Tx(ctx, func(ctx context.Context) error {
		if k.o.CurrentAccess != nil {
			p, d, err := k.o.CurrentAccess(ctx)
			if err != nil {
				return err
			}
			if d == nil {
				return httpkit.Denied(p)
			}
			ctx = accessapi.WithCaller(ctx, p, d)
		}
		return fn(ctx)
	})
	if err != nil {
		return fmt.Errorf("package publication transaction: %w", err)
	}
	return nil
}

func (k *Kit) writeTarget(c repo.Config, path string) item.Target {
	target := item.Target{Repo: c, Path: path}
	if k.o.CurrentAccess != nil {
		target.CheckCurrent = func(ctx context.Context, path string, folder bool, want accessapi.Actions) error {
			p, d, err := k.o.CurrentAccess(ctx)
			if err != nil {
				return err
			}
			if d == nil {
				return httpkit.Denied(p)
			}
			allows := d.Allows
			if folder {
				allows = d.AllowsFolder
			}
			if !allows(accessapi.ResourceArtifact, accessapi.Repo{Key: c.Key, Class: string(c.Class)}, path, want) {
				return fmt.Errorf("%w: %w", item.ErrPermissionDenied, httpkit.Denied(p))
			}
			return nil
		}
	}
	return target
}
