package upstream

import (
	"context"
	"errors"
	"net"
	"net/url"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
)

// destinationPolicy preserves remote-cache URL/matrix and error contracts while
// sharing DNS/IP rules with other platform HTTP users. private and lookup are
// immutable snapshots of the resolver's trusted settings.
type destinationPolicy struct {
	private map[string]bool
	lookup  func(context.Context, string) ([]net.IPAddr, error)
}

func (p *destinationPolicy) addresses(ctx context.Context, u *url.URL) ([]string, error) {
	if err := checkedURL(ctx, u); err != nil {
		return nil, err
	}
	private := make([]string, 0, len(p.private))
	for origin, allowed := range p.private {
		if allowed {
			private = append(private, origin)
		}
	}
	policy, err := outbound.NewPolicy(private, p.lookup)
	if err != nil {
		return nil, remote.ErrSecurity
	}
	addresses, err := policy.Addresses(ctx, u)
	switch {
	case err == nil:
		return addresses, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, safeError(err)
	case errors.Is(err, outbound.ErrConnection):
		return nil, remote.ErrConnection
	default:
		return nil, remote.ErrSecurity
	}
}
