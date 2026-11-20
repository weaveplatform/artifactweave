package dockerhttp

import (
	"context"
	"fmt"
	"net/http"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// tx acquires current authority before source revalidation or item locks. All
// upstream streaming belongs before this transaction; the context passed to fn
// holds the current caller and policy used for both source and destination.
func (g *Registry) tx(ctx context.Context, fn func(context.Context) error) error {
	err := g.store.Tx(ctx, func(ctx context.Context) error {
		if g.currentAccess != nil {
			p, d, err := g.currentAccess(ctx)
			if err != nil {
				return err
			}
			if d == nil {
				return Errorf(http.StatusForbidden, CodeUnauthorized, "current registry policy unavailable")
			}
			ctx = accessapi.WithCaller(ctx, p, d)
		}
		return fn(ctx)
	})
	if err != nil {
		return fmt.Errorf("registry publication transaction: %w", err)
	}
	return nil
}

func (g *Registry) checkImportRead(ctx context.Context, r *http.Request, read virtualRead) error {
	if g.currentAccess != nil {
		d, _ := accessapi.DeciderFrom(ctx)
		if !d.Allows(accessapi.ResourceArtifact, accessapi.Repo{Key: read.q.Repo.Key, Class: string(read.q.Repo.Class)}, read.q.Name+"/", accessapi.ActionRead) {
			p, _ := kernel.FromContext(ctx)
			return read.q.refuse(r.Method, p)
		}
	}
	if read.result.Revalidate != nil {
		if err := read.result.Revalidate(ctx); err != nil {
			return fmt.Errorf("registry import source authority: %w", err)
		}
	}
	return nil
}
