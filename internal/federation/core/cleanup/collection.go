package cleanup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
)

type (
	failureKey   struct{ instance, grant string }
	failureFence struct {
		mu     sync.Mutex
		epochs map[failureKey]failureEpoch
	}
)

type failureEpoch struct {
	value uint64
	at    time.Time
}

func (f *failureFence) current(id, grant string, now time.Time) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := failureKey{id, grant}
	value := f.epochs[key]
	if !value.at.IsZero() && now.Sub(value.at) > Window {
		delete(f.epochs, key)
		return 0
	}
	// A fresh scope can still reference this failure generation throughout its
	// own window. Retain an actively observed epoch until that window can end;
	// pruning by the original failure time would reset a current fence to zero.
	if !value.at.IsZero() && now.After(value.at) {
		value.at = now
		f.epochs[key] = value
	}
	return value.value
}

func (f *failureFence) record(id, grant string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.epochs == nil {
		f.epochs = map[failureKey]failureEpoch{}
	}
	// Every pre-failure observation is expired by this point. Pruning cannot
	// revive an old permit, and relationship churn retains no unbounded cache.
	for key, value := range f.epochs {
		if now.Sub(value.at) > Window {
			delete(f.epochs, key)
		}
	}
	key := failureKey{id, grant}
	value := f.epochs[key]
	value.value++
	value.at = now
	f.epochs[key] = value
}

func (f *failureFence) changed(members []api.CleanupMember, now time.Time) bool {
	for _, member := range members {
		if f.current(member.Instance, member.Grant, now) != member.LocalFailure {
			return true
		}
	}
	return false
}

type observation struct {
	member api.CleanupMember
	answer Answer
	err    error
}

// Collect shares one deadline across the entire batch and all members. A worker
// makes one attempt per member; failure never falls back to older statistics.
func (s *Service) Collect(ctx context.Context, scope api.CleanupScope, versions []api.CleanupVersion) (api.CleanupEvidence, error) {
	if s.Store.InTx(ctx) {
		return api.CleanupEvidence{}, fmt.Errorf("cleanup observation cannot retain a transaction: %w", ErrUnavailable)
	}
	if err := validateVersions(versions); err != nil {
		return api.CleanupEvidence{}, fmt.Errorf("collect cleanup observations: %w", err)
	}
	started := s.now()
	ctx, cancel := context.WithTimeout(ctx, Window)
	defer cancel()
	evidence := api.CleanupEvidence{Scope: scope, Started: started, Expires: started.Add(Window)}
	local, err := s.localDownloads(ctx, scope, versions)
	if err != nil {
		return api.CleanupEvidence{}, fmt.Errorf("collect cleanup observations: %w", err)
	}
	for _, row := range local {
		evidence.Pins = append(evidence.Pins, api.CleanupPin{Member: scope.Self, Incarnation: scope.Incarnation, Generation: scope.Generation, CleanupDownload: row})
	}
	if len(scope.Members) > 0 && s.Remote == nil {
		return api.CleanupEvidence{}, ErrUnavailable
	}
	challenge := rand.Text()
	jobs := make(chan api.CleanupMember, len(scope.Members))
	answers := make(chan observation, len(scope.Members))
	for _, member := range scope.Members {
		jobs <- member
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(Concurrency, len(scope.Members)) {
		workers.Go(func() {
			for member := range jobs {
				answer, err := s.collectMember(
					ctx,
					scope,
					member,
					Request{SourceRepository: scope.Repository, PackageType: scope.PackageType, Challenge: challenge, Versions: versions},
				)
				if err != nil {
					// Invalidate immediately, before another worker or a database wait
					// can delay the failure receipt.
					s.failures.record(member.Instance, member.Grant, s.now())
					revisions, revisionErr := s.revisions()
					if revisionErr == nil {
						revisionErr = revisions.ObserveFailure(ctx, member.Instance, member.Grant)
					}
					err = errors.Join(err, revisionErr)
				}
				answers <- observation{member: member, answer: answer, err: err}
			}
		})
	}
	workers.Wait()
	close(answers)
	byMember := map[string]Answer{}
	for result := range answers {
		if result.err != nil {
			err = errors.Join(err, ErrUnavailable, result.err)
		} else {
			byMember[result.member.Instance] = result.answer
		}
	}
	if err != nil {
		return api.CleanupEvidence{}, fmt.Errorf("member download observation: %w", err)
	}
	for _, member := range scope.Members {
		answer := byMember[member.Instance]
		for _, row := range answer.Versions {
			evidence.Pins = append(
				evidence.Pins,
				api.CleanupPin{
					Member:          member.Instance,
					Incarnation:     answer.Incarnation,
					Generation:      answer.Generation,
					Grant:           answer.Grant,
					ClockAllowance:  corepeer.Skew,
					CleanupDownload: row,
				},
			)
		}
	}
	if err := s.Store.Tx(ctx, func(ctx context.Context) error { return s.Guard(ctx, evidence) }); err != nil {
		return api.CleanupEvidence{}, fmt.Errorf("collect cleanup observations: %w", err)
	}
	if err := s.Fresh(ctx, evidence); err != nil {
		return api.CleanupEvidence{}, fmt.Errorf("collect cleanup observations: %w", err)
	}
	return evidence, nil
}

func (s *Service) collectMember(ctx context.Context, scope api.CleanupScope, member api.CleanupMember, request Request) (Answer, error) {
	var peer model.Peer
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.guardScope(ctx, scope); err != nil {
			return fmt.Errorf("current cleanup member scope: %w", err)
		}
		var err error
		peer, err = s.Store.Peers().Get(ctx, member.Instance)
		if err != nil {
			return fmt.Errorf("current cleanup peer: %w", err)
		}
		return nil
	})
	if err != nil {
		return Answer{}, fmt.Errorf("collect member observation: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, AttemptTimeout)
	defer cancel()
	answer, err := s.Remote.Downloads(ctx, peer, member.Repository, request)
	if err != nil {
		return Answer{}, fmt.Errorf("collect member observation: %w", err)
	}
	if answer.PackageType != scope.PackageType || answer.Instance != member.Instance || answer.Challenge != request.Challenge || answer.Incarnation == "" ||
		answer.Generation == "" ||
		answer.Grant == "" ||
		len(answer.Versions) != len(request.Versions) {
		return Answer{}, ErrUnavailable
	}
	// Return rows in request order and reject duplicate/unrequested identities.
	byIdentity := map[api.CleanupVersion]api.CleanupDownload{}
	for _, row := range answer.Versions {
		latest := s.now().Add(corepeer.Skew)
		if row.Created.After(latest) || row.LastDownloaded.After(latest) || row.AdmittedAt.After(latest) || row.Created.IsZero() && row.LastDownloaded.IsZero() {
			return Answer{}, ErrUnavailable
		}
		if row.ID <= 0 || row.Revision == "" || row.AdmissionRevision == "" || len(row.AdmissionRevision) > 256 || byIdentity[row.CleanupVersion].ID != 0 {
			return Answer{}, ErrUnavailable
		}
		byIdentity[row.CleanupVersion] = row
	}
	answer.Versions = make([]api.CleanupDownload, 0, len(request.Versions))
	for _, wanted := range request.Versions {
		row, found := byIdentity[wanted]
		if !found {
			return Answer{}, ErrUnavailable
		}
		answer.Versions = append(answer.Versions, row)
	}
	return answer, nil
}
