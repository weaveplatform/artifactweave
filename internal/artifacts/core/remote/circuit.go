package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// State returns the repository's upstream availability state as the
// circuit judges it under the credential revision, without writing it back:
// a state recorded under another revision loses its Smart negotiation,
// connection failures and cooldowns, and a repository whose assumed-offline
// period is zero or less is never offline. A store error comes back
// wrapped.
func (p *Policy) State(
	ctx context.Context,
	q source.Request,
	revision string,
) (model.RemoteSourceState, error) {
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

// Cooldown refuses an upstream exchange while state holds one off, by the
// policy's clock: a 429 *source.HTTPError carrying the rest of a Retry-After
// pause, or a 502 connectivity failure while the upstream is assumed
// offline. It returns nil when the exchange may go ahead.
func (p *Policy) Cooldown(state model.RemoteSourceState) error {
	now := p.now()
	if state.RetryAfterUntil.After(now) {
		return &source.HTTPError{
			Status:     429,
			Kind:       source.FailureKindHTTP,
			RetryAfter: state.RetryAfterUntil.Sub(now),
		}
	}
	if state.OfflineUntil.After(now) {
		return &source.HTTPError{Status: 502, Kind: source.FailureKindConnectivity}
	}
	return nil
}

func (p *Policy) changeState(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	change func(*model.RemoteSourceState),
) error {
	// Recording the outcome is bounded local bookkeeping, even when the
	// upstream consumed its deadline. It never initiates another network call.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	err := p.Store.Tx(ctx, func(tx context.Context) error {
		state, err := p.State(tx, q, revision)
		if err != nil {
			return err
		}
		if state.Source != permit.Identity {
			return kernel.ErrConflict
		}
		change(&state)
		return p.Store.RemoteCache().PutState(tx, q.Repo.Key, state)
	})
	if err != nil {
		return PolicyError(stateFault(ctx, err))
	}
	return nil
}

// stateFault is err, a failed read or write of the repository's
// availability state, marked as the server's storage fault when a
// statement the store ran failed (kernel.ErrStore): the read answers 500
// "internal error" and the router logs it, as a failed store call
// elsewhere in a remote read does (#1470), where it answered 502 as the
// upstream's failure. It passes through PolicyError as it is and the
// circuit does not count it. A store's verdict, such as kernel.ErrConflict
// for a source replaced since the permit, is left to answer as it did. 7.x
// keeps this state in memory, so no source says what a failed write of it
// answers [I] (https://github.com/weaveplatform/artifactweave/issues/1489).
func stateFault(ctx context.Context, err error) error {
	if !errors.Is(err, kernel.ErrStore) {
		return err
	}
	return item.MarkStorageFault(ctx, err, err) //nolint:wrapcheck // the store's own error, marked
}

// RecordFailure counts err against the repository's circuit when it is a
// connectivity or timeout *source.HTTPError, and ignores any other error.
// The tenth failure in a row marks the upstream offline for the
// assumed-offline period, 300 seconds when unset, and each failure after
// that starts the period again; a period of zero or less clears the count
// instead. The write is its own short transaction, bounded
// even when ctx has ended, and its error is classified by PolicyError:
// source.ErrSuperseded when the permit's source identity is no longer
// current.
func (p *Policy) RecordFailure(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	err error,
) error {
	var failure *source.HTTPError
	if !errors.As(err, &failure) ||
		(failure.Kind != source.FailureKindConnectivity && failure.Kind != source.FailureKindTimeout) {
		return nil
	}
	seconds := 300
	if q.Repo.AssumedOfflinePeriodSecs != nil {
		seconds = *q.Repo.AssumedOfflinePeriodSecs
	}
	return p.changeState(ctx, q, permit, revision, func(s *model.RemoteSourceState) {
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

// RecordResponse records that the upstream answered: any status clears the
// connection failures and cooldowns, and a 429 starts a Retry-After pause
// (from 1 to 300 seconds) that Cooldown then enforces. It writes as
// RecordFailure does, with the same errors.
func (p *Policy) RecordResponse(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	revision string,
	res *http.Response,
) error {
	return p.changeState(ctx, q, permit, revision, func(s *model.RemoteSourceState) {
		// Any HTTP response proves connectivity; authentication and application
		// failures keep their status and never become a connection outage.
		s.ConnectionFailures = 0
		s.OfflineUntil = time.Time{}
		s.RetryAfterUntil = time.Time{}
		if res.StatusCode == 429 {
			s.RetryAfterUntil = p.now().Add(RetryAfter(res.Header.Get("Retry-After"), p.now()))
		}
	})
}
