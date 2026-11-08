package smart

import (
	"context"
	"io"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
)

// enabled reports a repository switch that is set and on, as core remote's
// own helper does; an unset switch is off.
func enabled(v *bool) bool { return v != nil && *v }

// policyNow is the shared policy's clock as (*remote.Policy).now reads it,
// which this package cannot call: the injected Now first, else the wall
// clock, in UTC. It must stay in step with that method, or Smart's discovery
// cache, queue leases and synchronisation times would be judged against
// another clock than the policy's own.
func policyNow(p *remote.Policy) time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// cancelBody releases a peer request's timeout and metadata contexts when its
// body is closed, as core remote's own type does for the transport. Close
// returns the raw close error rather than core's sanitised one: every Smart
// caller discards it.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	b.cancel()
	//nolint:wrapcheck // every caller discards the close error
	return b.ReadCloser.Close()
}
