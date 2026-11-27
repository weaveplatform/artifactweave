package federated

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Configuration reads the masked configuration for a current federation member.
// Recheck trust and membership inside the transaction: a request verified before
// a revocation must not disclose the configuration after that revocation.
func (r Receiver) Configuration(ctx context.Context, caller model.Peer, key string) (map[string]any, error) {
	var result map[string]any
	err := r.Membership.Store.Tx(ctx, func(ctx context.Context) error {
		current, err := r.Membership.Store.Peers().Get(ctx, caller.InstanceID)
		if errors.Is(err, kernel.ErrNotFound) {
			return kernel.ErrForbidden
		}
		if err != nil {
			return fmt.Errorf("configuration reader peer: %w", err)
		}
		if !bytes.Equal(current.PublicKey, caller.PublicKey) {
			return kernel.ErrForbidden
		}
		if !current.Compatible() || !current.Has(model.UseFederatedRepo) && !current.Has(model.UseFederatedRepoRead) {
			return kernel.ErrForbidden
		}
		members, err := r.Membership.Store.Federation().Members(ctx, key)
		if err != nil {
			return fmt.Errorf("configuration reader members: %w", err)
		}
		member := false
		for _, m := range members {
			member = member || m.PeerInstanceID == current.InstanceID
		}
		if !member {
			return kernel.ErrForbidden
		}
		cfg, err := r.Repositories.Get(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			return kernel.ErrForbidden
		}
		if err != nil {
			return fmt.Errorf("configuration reader repository: %w", err)
		}
		if cfg.Class != repo.Federated {
			return kernel.ErrForbidden
		}
		self, err := r.Membership.SelfBase(ctx)
		if err != nil {
			return err
		}
		result, err = Payload(cfg, self)
		return err
	})
	return result, err //nolint:wrapcheck // each read names its cause
}

// Inconsistent compares shared configurations of reachable current members.
// A transport failure belongs to unavailableMirrors, not to a drift report.
func (m Monitoring) Inconsistent(ctx context.Context) ([]string, error) {
	type probe struct {
		key      string
		member   model.FederationMember
		peer     model.Peer
		expected []byte
	}
	probes := []probe{}
	err := m.tx(ctx, func(ctx context.Context) error {
		cfgs, err := m.Repositories.List(ctx, artifactsapi.RepoFilter{})
		if err != nil {
			return fmt.Errorf("configuration drift repositories: %w", err)
		}
		self, err := m.Membership.SelfBase(ctx)
		if err != nil {
			return err
		}
		for _, cfg := range cfgs {
			if cfg.Class != repo.Federated {
				continue
			}
			body, err := Payload(cfg, self)
			if err != nil {
				return err
			}
			expected, err := configurationIdentity(body)
			if err != nil {
				return err
			}
			members, err := m.Membership.Store.Federation().Members(ctx, cfg.Key)
			if err != nil {
				return fmt.Errorf("configuration drift members: %w", err)
			}
			for _, member := range members {
				peer, err := m.Membership.Store.Peers().Get(ctx, member.PeerInstanceID)
				if err != nil {
					return fmt.Errorf("configuration drift peer: %w", err)
				}
				if peer.Compatible() {
					probes = append(probes, probe{key: cfg.Key, member: member, peer: peer, expected: expected})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := []string{}
	if len(probes) == 0 {
		return result, nil
	}
	var remote ConfigurationRemote
	if m.Remote != nil {
		remote = m.Remote()
	}
	if remote == nil {
		return nil, ErrNoStreams
	}
	for _, p := range probes {
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		actual, err := remote.Configuration(call, p.peer, p.member.RemoteRepoKey)
		cancel()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("federation configuration comparison: %w", ctx.Err())
		}
		if err != nil {
			continue
		}
		identity, err := configurationIdentity(actual)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(p.expected, identity) && !slices.Contains(result, p.key) {
			result = append(result, p.key)
		}
	}
	// Network I/O can outlast a credential change. Refresh authority before
	// disclosing the report, as the initial read does before collecting it.
	if err := m.Administrator(ctx); err != nil {
		return nil, err
	}
	slices.Sort(result)
	return result, nil
}

func configurationIdentity(body map[string]any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode member configuration: %w", err)
	}
	var cfg repo.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode member configuration: %w", err)
	}
	repo.Defaults(&cfg)
	for i := range cfg.Members {
		if cfg.Members[i].Mode == "" {
			cfg.Members[i].Mode = repo.ModeBidirectional
		}
		if cfg.Members[i].Enabled == nil {
			cfg.Members[i].Enabled = repo.Flex(true)
		}
	}
	slices.SortFunc(cfg.Members, func(a, b repo.Member) int { return cmp.Compare(a.URL, b.URL) })
	shared, err := Payload(cfg, "")
	if err != nil {
		return nil, err
	}
	// Repository keys are local to each member and can deliberately differ.
	delete(shared, "key")
	raw, err = json.Marshal(shared)
	if err != nil {
		return nil, fmt.Errorf("encode shared configuration identity: %w", err)
	}
	return raw, nil
}
