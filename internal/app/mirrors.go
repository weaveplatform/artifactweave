package app

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/blob/mirror"
)

// mirrorAdmin gives the storage endpoints the runtime's mirrors (#18): the
// optimize trigger and their status.
type mirrorAdmin struct {
	tiers []mirrorTier
}

type mirrorTier struct {
	name    string
	store   *mirror.Store
	trigger chan struct{}
}

// mirrorsAdmin returns the mirrors for server.Config, or a nil interface when
// no tier is a mirror (a nil *mirrorAdmin in the interface would not be nil).
func (rt *runtime) mirrorsAdmin() server.Mirrors {
	if rt.admin == nil {
		return nil
	}
	return rt.admin
}

// Optimize asks each mirror's repair loop for a pass now. The send never
// blocks: a pass already queued answers this request too.
func (a *mirrorAdmin) Optimize() {
	for _, t := range a.tiers {
		select {
		case t.trigger <- struct{}{}:
		default:
		}
	}
}

func (a *mirrorAdmin) Status(ctx context.Context) ([]server.MirrorStatus, error) {
	out := make([]server.MirrorStatus, 0, len(a.tiers))
	for _, t := range a.tiers {
		st, err := t.store.Status(ctx)
		if err != nil {
			return nil, err //nolint:wrapcheck // mirror errors name the package
		}
		out = append(out, server.MirrorStatus{Tier: t.name, Status: st})
	}
	return out, nil
}
