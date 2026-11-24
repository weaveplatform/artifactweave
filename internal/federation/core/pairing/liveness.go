package pairing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
)

// This file is the liveness half of the peer records (#1949, decision
// 0021, Wire contract): the signed GET /status read this instance serves
// (Describe) and the periodic read of every paired peer's (Probe) that
// keeps each record's status, last-seen time and clock skew current.

// DefaultProbeInterval is how often Probe runs when the configuration sets
// no interval: StaleAfter's default, ten minutes, is two of them, so one
// lost read never shows a reachable peer OFFLINE.
const DefaultProbeInterval = defaultStaleAfter / staleProbes

// staleProbes is how many probe intervals a peer may go unseen before it
// is OFFLINE (StaleAfterProbes).
const staleProbes = 2

// probeParallel bounds the peers one Probe reads at once, so a large
// federation's reads neither open a connection per peer at once nor run
// one at a time behind an unreachable peer's timeouts.
const probeParallel = 8

// StaleAfterProbes is the staleness threshold for a probe interval: two
// intervals, so a peer is OFFLINE once two reads in a row found it
// unreachable, and a single lost read changes nothing.
func StaleAfterProbes(interval time.Duration) time.Duration { return staleProbes * interval }

// PeerStatus is what a peer's GET /status answers (the peer API's
// PeerStatus): its identity, versions, clock and the use cases it holds
// with the caller.
type PeerStatus struct {
	InstanceID      string
	ProtocolVersion string
	Version         string
	Time            time.Time
	UseCases        []model.UseCase
}

// StatusRemote reads a peer's signed GET /status. The peer adapter
// implements it with the signing client, which verifies the answer with
// the key recorded for the peer before Status returns it. Its errors are
// ErrUnverified when the trust no longer holds, either way round: an
// answer carrying a signature the recorded key does not verify, or the
// peer's 401 refusing this instance's signature; a *StatusError for any
// other answer that is not a verified 200; and ErrUnreachable when no
// answer came.
type StatusRemote interface {
	Status(ctx context.Context, peer model.Peer) (PeerStatus, error)
}

// Describe is this instance's answer to a verified peer's GET /status:
// its identity and protocol version, version as the release, its clock
// now, and the use cases the caller holds here, from the record the
// transport verified the caller with. A caller recorded as
// model.DeliveryPulled is one this instance cannot reach, so Probe never
// reads it; its own verified read is what shows it alive, and is recorded
// here as Probe records an answer.
func (s Service) Describe(ctx context.Context, caller model.Peer, version string) (PeerStatus, error) {
	id, err := s.identity(ctx)
	if err != nil {
		return PeerStatus{}, err
	}
	now := s.now()
	if caller.Delivery == model.DeliveryPulled {
		caller.LastSeen = now
		l := model.Liveness{
			Status: caller.DeriveStatus(now, s.staleAfter(), false, 0), LastSeen: now, ClockSkew: caller.ClockSkew,
		}
		if err := s.Store.Peers().SetLiveness(ctx, caller.InstanceID, l); err != nil {
			return PeerStatus{}, fmt.Errorf("record peer %s seen: %w", caller.InstanceID, err)
		}
	}
	uses := caller.UseCases
	if uses == nil {
		uses = []model.UseCase{}
	}
	return PeerStatus{
		InstanceID: id.InstanceID, ProtocolVersion: Version(id.ProtocolVersion), Version: version,
		Time: now, UseCases: uses,
	}, nil
}

// Probe reads every paired peer's GET /status and records the outcome on
// its record (PeerStore.SetLiveness). A verified answer records the peer
// as seen now, with the skew between its clock and this one's; an answer
// whose signature does not verify, or the peer's refusal of this
// instance's, shows the peer UNAUTHORIZED, as decision 0021 has a peer
// whose signatures stop verifying; no answer, or any other answer, leaves
// the last-seen time as it was, so the peer turns OFFLINE once it is
// older than StaleAfter. UNAUTHORIZED lasts until an answer verifies again.
// A peer recorded as model.DeliveryPulled cannot be reached from here and
// is not read: its status follows its own reads of this instance
// (Describe). Probe fails only when a record cannot be read or written;
// what each peer answered is in its record.
func (s Service) Probe(ctx context.Context) error {
	if s.Prober == nil {
		return nil
	}
	peers, err := s.Store.Peers().List(ctx, port.PeerFilter{})
	if err != nil {
		return fmt.Errorf("list peers: %w", err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		slot = make(chan struct{}, probeParallel)
	)
	for _, p := range peers {
		slot <- struct{}{}
		wg.Go(func() {
			defer func() { <-slot }()
			if err := s.probe(ctx, p); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// probe reads one peer's status and records the outcome.
func (s Service) probe(ctx context.Context, p model.Peer) error {
	l := p.Liveness()
	rejected := p.Status == model.PeerUnauthorized
	if p.Pushes() {
		sent := s.now()
		ans, err := s.Prober.Status(ctx, p)
		received := s.now()
		switch {
		case err == nil:
			rejected = false
			l.LastSeen = received
			// The peer read its clock between sending and receiving; the
			// midpoint is the best estimate of when, without the peer's
			// own timing.
			l.ClockSkew = ans.Time.Sub(sent.Add(received.Sub(sent) / 2))
		case errors.Is(err, ErrUnverified):
			rejected = true
		}
		p.LastSeen = l.LastSeen
	}
	l.Status = p.DeriveStatus(s.now(), s.staleAfter(), rejected, 0)
	if same(l, p.Liveness()) {
		return nil
	}
	if err := s.Store.Peers().SetLiveness(ctx, p.InstanceID, l); err != nil {
		return fmt.Errorf("record peer %s liveness: %w", p.InstanceID, err)
	}
	return nil
}

// same reports whether a and b record the same liveness, so an unchanged
// record is not written every interval.
func same(a, b model.Liveness) bool {
	return a.Status == b.Status && a.LastSeen.Equal(b.LastSeen) && a.ClockSkew == b.ClockSkew
}
