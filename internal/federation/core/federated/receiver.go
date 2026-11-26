package federated

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// AllowsPeer enforces the direction of receiving-only trust independently
// of membership. Two-way trust takes precedence when both grants exist.
func (Stream) AllowsPeer(p model.Peer, d streamsync.Direction) bool {
	if p.Has(model.UseFederatedRepo) {
		return true
	}
	return p.Has(model.UseFederatedRepoRead) && (d == streamsync.Receive) == p.ReceivesFederationOnly
}

// AllowsStream checks each member's role at every authorization, including
// inside apply transactions. Receivers cannot supply events or snapshots.
func (s Stream) AllowsStream(ctx context.Context, stream string, p model.Peer, d streamsync.Direction) (bool, error) {
	k, ok := key(stream)
	if !ok {
		return false, nil
	}
	if d == streamsync.Send {
		receiver, err := s.receivingOnly(ctx, k)
		return !receiver, err
	}
	members, err := s.Store.Federation().Members(ctx, k)
	if err != nil {
		return false, fmt.Errorf("members of %s: %w", k, err)
	}
	for _, m := range members {
		if m.PeerInstanceID == p.InstanceID {
			return m.Mode != repo.ModeReceiver, nil
		}
	}
	return false, nil
}

func selfMemberDisabled(c repo.Config, self string) bool {
	return slices.ContainsFunc(c.Members, func(m repo.Member) bool {
		return model.IsSelfMember(m.URL, c.Key, self) && m.Enabled != nil && !m.Enabled.Bool()
	})
}

func selfReceiver(c repo.Config, self string) bool {
	return slices.ContainsFunc(c.Members, func(m repo.Member) bool {
		return m.Mode == repo.ModeReceiver && model.IsSelfMember(m.URL, c.Key, self)
	})
}

func (s Stream) receivingOnly(ctx context.Context, k string) (bool, error) {
	if s.Repositories == nil || s.SelfBase == nil {
		return false, nil
	}
	c, err := s.Repositories.Get(ctx, k)
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("repository %s: %w", k, err)
	}
	self, err := s.SelfBase(ctx)
	if err != nil {
		return false, err
	}
	return selfReceiver(c, self), nil
}

// AuthoritativeSnapshot makes a sender's full sync reconcile a receiver's
// divergent local writes, including items that exist only on the receiver.
// Those writes carry no stream stamps and never participate in conflicts.
func (s Stream) AuthoritativeSnapshot(ctx context.Context, stream string) (bool, error) {
	k, ok := key(stream)
	if !ok {
		return false, nil
	}
	return s.receivingOnly(ctx, k)
}

// CheckLocalUpdate prevents an operator on a receiver from changing its
// membership, including removing its receiver role. Trusted configuration
// sync uses Admit directly and can update the role from a sending member.
func (m Membership) CheckLocalUpdate(ctx context.Context, next repo.Config, get func(context.Context, string) (repo.Config, error)) error {
	before, err := get(ctx, next.Key)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read repository %s: %w", next.Key, err)
	}
	if before.Class != repo.Federated {
		return nil
	}
	self, err := m.SelfBase(ctx)
	if err != nil {
		return err
	}
	if !selfReceiver(before, self) {
		return nil
	}
	if next.Class != repo.Federated || !equalMembers(before, next, self) {
		return refuse("Repository '%s' is in receiver mode: its members cannot be changed locally", next.Key)
	}
	return nil
}

func equalMembers(a, b repo.Config, self string) bool {
	canonical := func(c repo.Config) []string {
		out := make([]string, 0, len(c.Members))
		for _, m := range c.Members {
			u := m.URL
			if model.IsSelfMember(u, c.Key, self) {
				u = repo.SelfMemberURL(c.Key)
			} else if base, key, err := model.ParseMemberURL(u); err == nil {
				u = repo.MemberURL(base, key)
			}
			mode := m.Mode
			if mode == "" {
				mode = repo.ModeBidirectional
			}
			out = append(out, fmt.Sprintf("%s|%s|%t", u, mode, m.Enabled == nil || m.Enabled.Bool()))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(canonical(a), canonical(b))
}
