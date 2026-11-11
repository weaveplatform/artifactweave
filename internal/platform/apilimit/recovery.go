package apilimit

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// EnterRecovery reserves two requests and sixty authentication attempts per
// minute in this process for the narrowly mounted administrative recovery API.
// Its constant-size state is independent of metadata budget capacity/failure.
// Recovery still requires valid administrator credentials and a working store.
func (s *Service) EnterRecovery(ctx context.Context) (func(), Decision, error) {
	if err := ctx.Err(); err != nil {
		return nil, Decision{}, fmt.Errorf("admit recovery: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if !s.recoveryReset.After(now) {
		s.recoveryUsed = 0
		s.recoveryReset = now.Truncate(time.Minute).Add(time.Minute)
	}
	d := Decision{
		Policy: "recovery", Limit: 60, Remaining: max(0, 60-s.recoveryUsed),
		Reset: s.recoveryReset, ResetAfter: s.recoveryReset.Sub(now),
	}
	if s.recoveryUsed >= 60 {
		d.RetryAfter = d.ResetAfter
		return nil, d, nil
	}
	if s.recovery >= 2 {
		return nil, d, ErrBusy
	}
	s.recovery++
	s.recoveryUsed++
	d.Allowed, d.Remaining = true, d.Remaining-1
	return sync.OnceFunc(func() { s.mu.Lock(); s.recovery--; s.mu.Unlock() }), d, nil
}
