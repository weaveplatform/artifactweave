package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (p *Policy) state(
	ctx context.Context,
	q source.Request,
	revision string,
) (types.RemoteSourceState, error) {
	state, err := p.Store.RemoteCache().State(ctx, q.Repo.Key)
	if err != nil {
		return state, fmt.Errorf("read remote availability: %w", err)
	}
	if state.SettingsRevision != revision {
		state.Smart = nil
		state.ConnectionFailures = 0
		state.OfflineUntil = time.Time{}
		state.RetryAfterUntil = time.Time{}
		state.SettingsRevision = revision
	}
	if q.Repo.AssumedOfflinePeriodSecs != nil && *q.Repo.AssumedOfflinePeriodSecs <= 0 {
		state.ConnectionFailures = 0
		state.OfflineUntil = time.Time{}
	}
	return state, nil
}

func (p *Policy) cooldown(state types.RemoteSourceState) error {
	now := p.now()
	if state.RetryAfterUntil.After(now) {
		return &source.HTTPError{
			Status:     429,
			Kind:       source.HTTPFailure,
			RetryAfter: state.RetryAfterUntil.Sub(now),
		}
	}
	if state.OfflineUntil.After(now) {
		return &source.HTTPError{Status: 502, Kind: source.ConnectivityFailure}
	}
	return nil
}

func (p *Policy) changeState(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	change func(*types.RemoteSourceState),
) error {
	// Recording the outcome is bounded local bookkeeping, even when the
	// upstream consumed its deadline. It never initiates another network call.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	err := p.Store.Tx(ctx, func(tx context.Context) error {
		state, err := p.state(tx, q, revision)
		if err != nil {
			return err
		}
		if state.Source != permit.Identity {
			return store.ErrConflict
		}
		change(&state)
		return p.Store.RemoteCache().PutState(tx, q.Repo.Key, state)
	})
	if err != nil {
		return policyError(err)
	}
	return nil
}

func (p *Policy) recordFailure(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	err error,
) error {
	var failure *source.HTTPError
	if !errors.As(err, &failure) ||
		(failure.Kind != source.ConnectivityFailure && failure.Kind != source.TimeoutFailure) {
		return nil
	}
	seconds := 300
	if q.Repo.AssumedOfflinePeriodSecs != nil {
		seconds = *q.Repo.AssumedOfflinePeriodSecs
	}
	return p.changeState(ctx, q, permit, revision, func(s *types.RemoteSourceState) {
		if seconds <= 0 {
			s.ConnectionFailures = 0
			s.OfflineUntil = time.Time{}
			return
		}
		if !s.OfflineUntil.IsZero() || s.ConnectionFailures >= 9 {
			s.ConnectionFailures = 10
			s.OfflineUntil = p.now().Add(time.Duration(seconds) * time.Second)
		} else {
			s.ConnectionFailures++
		}
	})
}

func (p *Policy) recordResponse(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	res *http.Response,
) error {
	return p.changeState(ctx, q, permit, revision, func(s *types.RemoteSourceState) {
		// Any HTTP response proves connectivity; authentication and application
		// failures keep their status and never become a connection outage.
		s.ConnectionFailures = 0
		s.OfflineUntil = time.Time{}
		s.RetryAfterUntil = time.Time{}
		if res.StatusCode == 429 {
			s.RetryAfterUntil = p.now().Add(retryAfter(res.Header.Get("Retry-After"), p.now()))
		}
	})
}
