package replication

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// Run retains the target reservation for a transfer outside transactions. The
// queue owns the run heartbeat; this heartbeat checks both generations and
// renews only the target. Losing either cancels network work and prevents a nil
// callback result from turning lease loss into completion. Every publication
// must still call Fence inside its own final transaction.
func (o Ownership) Run(ctx context.Context, item work.WorkItem, target string,
	transfer func(context.Context, Owner) error,
) (err error) {
	if transfer == nil || o.duration()/3 <= 0 {
		return fmt.Errorf("replication transfer callback is unavailable: %w", kernel.ErrInvalid)
	}
	owner, err := o.Claim(ctx, item, target)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	failure := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(o.duration() / 3)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := o.Renew(runCtx, owner); err != nil {
					// Normal callback completion also cancels this context. An
					// interrupted heartbeat must not turn that success into retry.
					if runCtx.Err() == nil {
						failure <- fmt.Errorf("replication target renewal ended: %w", err)
						cancel()
					}
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-done
		select {
		case renewal := <-failure:
			err = errors.Join(err, renewal)
		default:
		}
		// Shutdown must release the reservation even after the network context
		// was cancelled. A successor's generation is never released or revived.
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer releaseCancel()
		if released := o.Release(releaseCtx, owner); released != nil && !errors.Is(released, kernel.ErrConflict) {
			err = errors.Join(err, released)
		}
	}()
	err = transfer(runCtx, owner)
	if stopped := ctx.Err(); stopped != nil {
		err = errors.Join(err, fmt.Errorf("replication transfer context ended: %w", stopped))
	}
	return err
}
