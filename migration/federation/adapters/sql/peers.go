package sql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/federation/core/model"
	"github.com/weaveplatform/artifactweave/migration/federation/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	retentionapi "github.com/weaveplatform/artifactweave/migration/retention/api"
)

// Peers returns the peer store: paired instances and the pairing nonces
// that admit new ones (decision 0021).
func (s *Store) Peers() port.PeerStore { return peerStore{s} }

type peerStore struct{ s *Store }

const peerCols = "instance_id, base_url, public_key, protocol_version, status, last_seen_at, " +
	"display_name, city_name, country_code, edge_role"

func scanPeer(r dialect.PeersScanner) (model.Peer, error) {
	var (
		p    model.Peer
		pub  []byte
		seen sql.NullInt64
	)
	err := r.Scan(&p.InstanceID, &p.BaseURL, &pub, &p.ProtocolVersion, &p.Status, &seen,
		&p.DisplayName, &p.CityName, &p.CountryCode, &p.EdgeRole)
	if len(pub) > 0 {
		p.PublicKey = pub
	}
	p.LastSeen = dialect.FromMicros(seen)
	return p, err //nolint:wrapcheck // wrapped by the caller
}

func (p peerStore) Put(ctx context.Context, peer model.Peer) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.Exec(
			ctx,
			"put peer "+peer.InstanceID,
			dialect.Upsert("peers", strings.Split(peerCols, ", "), []string{"instance_id"}),
			peer.InstanceID,
			peer.BaseURL,
			[]byte(peer.PublicKey),
			peer.ProtocolVersion,
			string(peer.Status),
			dialect.Micros(peer.LastSeen),
			peer.DisplayName,
			peer.CityName,
			peer.CountryCode,
			peer.EdgeRole,
		); err != nil {
			return err
		}
		if _, err := p.s.Exec(ctx, "put peer use cases",
			"DELETE FROM peer_use_cases WHERE peer = ?", peer.InstanceID); err != nil {
			return err
		}
		for _, u := range peerUseCaseSet(peer.UseCases) {
			if _, err := p.s.Exec(
				ctx,
				"put peer use cases",
				"INSERT INTO peer_use_cases (peer, use_case) VALUES (?, ?)",
				peer.InstanceID,
				string(u),
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// peerUseCaseSet sorts and deduplicates use cases, the form they are stored
// in.
func peerUseCaseSet(in []model.UseCase) []model.UseCase {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func (p peerStore) Get(ctx context.Context, instanceID string) (model.Peer, error) {
	peer, err := scanPeer(p.s.Q(ctx).QueryRowContext(ctx,
		p.s.Dialect().Rebind("SELECT "+peerCols+" FROM peers WHERE instance_id = ?"), instanceID))
	if err != nil {
		return model.Peer{}, p.s.Wrap("get peer "+instanceID, err)
	}
	uses, err := p.useCases(ctx, "WHERE peer = ?", instanceID)
	if err != nil {
		return model.Peer{}, err
	}
	peer.UseCases = uses[instanceID]
	return peer, nil
}

// useCases reads peer_use_cases rows matching where, grouped by peer and
// sorted.
func (p peerStore) useCases(
	ctx context.Context,
	where string,
	args ...any,
) (map[string][]model.UseCase, error) {
	out := map[string][]model.UseCase{}
	err := p.s.Rows(ctx, "list peer use cases",
		"SELECT peer, use_case FROM peer_use_cases "+where+" ORDER BY peer, use_case", args,
		func(r *sql.Rows) error {
			var peer, use string
			if err := r.Scan(&peer, &use); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			out[peer] = append(out[peer], model.UseCase(use))
			return nil
		})
	return out, err
}

func (p peerStore) List(ctx context.Context, f port.PeerFilter) ([]model.Peer, error) {
	where, args := "", []any(nil)
	if f.UseCase != "" {
		where = " WHERE instance_id IN (SELECT peer FROM peer_use_cases WHERE use_case = ?)"
		args = []any{string(f.UseCase)}
	}
	var out []model.Peer
	err := p.s.Rows(
		ctx,
		"list peers",
		"SELECT "+peerCols+" FROM peers"+where+" ORDER BY instance_id",
		args,
		func(r *sql.Rows) error {
			peer, err := scanPeer(r)
			out = append(out, peer)
			return err
		},
	)
	if err != nil || len(out) == 0 {
		return out, err
	}
	uses, err := p.useCases(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].UseCases = uses[out[i].InstanceID]
	}
	return out, nil
}

func (p peerStore) Delete(ctx context.Context, instanceID string) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		// Explicit rather than relying on ON DELETE CASCADE, which SQLite
		// honours only with the foreign_keys pragma on.
		if _, err := p.s.Exec(ctx, "delete peer use cases",
			"DELETE FROM peer_use_cases WHERE peer = ?", instanceID); err != nil {
			return err
		}
		return p.s.ExecOne(
			ctx,
			"delete peer "+instanceID,
			"DELETE FROM peers WHERE instance_id = ?",
			instanceID,
		)
	})
}

func (p peerStore) AddNonce(ctx context.Context, n model.PairingNonce) error {
	_, err := p.s.Exec(
		ctx,
		"add pairing nonce",
		"INSERT INTO pairing_nonces (nonce_hash, use_case, expires_at, redeemed_at) VALUES (?, ?, ?, ?)",
		n.NonceHash,
		string(n.UseCase),
		dialect.Micros(n.ExpiresAt),
		dialect.Micros(n.RedeemedAt),
	)
	return err
}

func (p peerStore) RedeemNonce(
	ctx context.Context,
	hash []byte,
	now time.Time,
) (model.PairingNonce, error) {
	var out model.PairingNonce
	err := p.s.Tx(ctx, func(ctx context.Context) error {
		// The conditional update is the redemption: of two concurrent
		// redeemers exactly one changes the row.
		n, err := p.s.Exec(
			ctx,
			"redeem pairing nonce",
			"UPDATE pairing_nonces SET redeemed_at = ? "+
				"WHERE nonce_hash = ? AND redeemed_at IS NULL AND expires_at > ?",
			now.UnixMicro(),
			hash,
			now.UnixMicro(),
		)
		if err != nil {
			return err
		}
		var exp, red sql.NullInt64
		var use string
		err = p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
			"SELECT use_case, expires_at, redeemed_at FROM pairing_nonces WHERE nonce_hash = ?",
		), hash).
			Scan(&use, &exp, &red)
		if err != nil {
			return p.s.Wrap(fmt.Sprintf("redeem pairing nonce %x", hash), err)
		}
		out = model.PairingNonce{
			NonceHash: hash, UseCase: model.UseCase(use),
			ExpiresAt: dialect.FromMicros(exp), RedeemedAt: dialect.FromMicros(red),
		}
		switch {
		case n == 1:
			return nil
		case red.Valid:
			return fmt.Errorf("pairing nonce %x already redeemed: %w", hash, kernel.ErrConflict)
		default:
			return fmt.Errorf("pairing nonce %x expired: %w", hash, kernel.ErrNotFound)
		}
	})
	if err != nil {
		return model.PairingNonce{}, err
	}
	return out, nil
}

func (p peerStore) PruneNonces(ctx context.Context, before time.Time) (int64, error) {
	return p.s.Exec(ctx, "prune pairing nonces",
		"DELETE FROM pairing_nonces WHERE expires_at IS NULL OR expires_at < ?", before.UnixMicro())
}

// Federation returns the federated repository store: members and full
// syncs (decision 0022).
func (s *Store) Federation() port.FederationStore { return federationStore{s} }

type federationStore struct{ s *Store }

const fedMemberCols = "repo_key, peer, base_url, remote_repo_key, enabled"

func (f federationStore) SetMembers(
	ctx context.Context,
	repoKey string,
	members []model.FederationMember,
) error {
	for _, m := range members {
		if m.PeerInstanceID == "" {
			return fmt.Errorf(
				"federation member %q of %s has no peer: %w",
				m.BaseURL,
				repoKey,
				kernel.ErrInvalid,
			)
		}
	}
	return f.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := f.s.Exec(ctx, "set federation members",
			"DELETE FROM federation_members WHERE repo_key = ?", repoKey); err != nil {
			return err
		}
		for _, m := range members {
			if _, err := f.s.Exec(
				ctx,
				"set federation members",
				dialect.Insert("federation_members", strings.Split(fedMemberCols, ", ")),
				repoKey,
				m.PeerInstanceID,
				m.BaseURL,
				m.RemoteRepoKey,
				m.Enabled,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (f federationStore) members(
	ctx context.Context,
	where, order string,
	arg string,
) ([]model.FederationMember, error) {
	var out []model.FederationMember
	err := f.s.Rows(
		ctx,
		"list federation members",
		"SELECT "+fedMemberCols+" FROM federation_members WHERE "+where+" = ? ORDER BY "+order,
		[]any{arg},
		func(r *sql.Rows) error {
			var m model.FederationMember
			err := r.Scan(&m.RepoKey, &m.PeerInstanceID, &m.BaseURL, &m.RemoteRepoKey, &m.Enabled)
			out = append(out, m)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	return out, err
}

func (f federationStore) Members(
	ctx context.Context,
	repoKey string,
) ([]model.FederationMember, error) {
	return f.members(ctx, "repo_key", "peer", repoKey)
}

func (f federationStore) MembersWithPeer(
	ctx context.Context,
	peer string,
) ([]model.FederationMember, error) {
	return f.members(ctx, "peer", "repo_key", peer)
}

const fullSyncCols = "repo_key, peer, forced, snapshot_seq, started_at, finished_at, status, result_paths"

func scanFullSync(r dialect.PeersScanner) (model.FullSyncRun, error) {
	var (
		run               model.FullSyncRun
		started, finished sql.NullInt64
		status            string
		paths             sql.NullString
	)
	if err := r.Scan(&run.ID, &run.RepoKey, &run.PeerInstanceID, &run.Forced, &run.SnapshotSeq,
		&started, &finished, &status, &paths); err != nil {
		return run, err //nolint:wrapcheck // wrapped by the caller
	}
	run.Started, run.Finished, run.Status = dialect.FromMicros(
		started,
	), dialect.FromMicros(
		finished,
	), retentionapi.RunStatus(
		status,
	)
	return run, dialect.FromJSON(paths, &run.ResultPaths)
}

// fullSyncArgs returns the column values of r after the id.
func fullSyncArgs(r model.FullSyncRun) []any {
	return []any{
		r.RepoKey, r.PeerInstanceID, r.Forced, r.SnapshotSeq,
		dialect.Micros(r.Started), dialect.Micros(r.Finished), string(r.Status),
		peersDoc(r.ResultPaths),
	}
}

// peersDoc encodes a document column of this group. Every document here is a
// string slice, a map of string slices or a model.PatternFilter, none of
// which can fail to marshal, so the error dialect.ToJSON reports for other types
// cannot occur.
func peersDoc(v any) any {
	doc, _ := dialect.ToJSON(v)
	return doc
}

func (f federationStore) CreateFullSync(
	ctx context.Context,
	r model.FullSyncRun,
) (model.FullSyncRun, error) {
	var err error
	if r.ID, err = f.s.InsertID(
		ctx,
		"create full sync",
		dialect.Insert(
			"full_sync_runs",
			strings.Split(fullSyncCols, ", "),
		),
		fullSyncArgs(r)...); err != nil {
		return model.FullSyncRun{}, err
	}
	return kernel.Normalise(r), nil
}

func (f federationStore) FullSync(ctx context.Context, id int64) (model.FullSyncRun, error) {
	r, err := scanFullSync(f.s.Q(ctx).QueryRowContext(ctx, f.s.Dialect().Rebind(
		"SELECT id, "+fullSyncCols+" FROM full_sync_runs WHERE id = ?"), id))
	if err != nil {
		return model.FullSyncRun{}, f.s.Wrap(fmt.Sprintf("get full sync %d", id), err)
	}
	return r, nil
}

func (f federationStore) UpdateFullSync(ctx context.Context, r model.FullSyncRun) error {
	return f.s.ExecOne(ctx, fmt.Sprintf("update full sync %d", r.ID), "UPDATE full_sync_runs SET "+
		"repo_key = ?, peer = ?, forced = ?, snapshot_seq = ?, started_at = ?, finished_at = ?, "+
		"status = ?, result_paths = ? WHERE id = ?", append(fullSyncArgs(r), r.ID)...)
}

func (f federationStore) FullSyncs(
	ctx context.Context, repoKey string, p kernel.Page,
) ([]model.FullSyncRun, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := dialect.Keyset(p, []string{"id"}, map[string]bool{"id": true}, true)
	if err != nil {
		return nil, "", err
	}
	if repoKey != "" {
		cond += " AND repo_key = ?"
		args = append(args, repoKey)
	}
	var out []model.FullSyncRun
	err = f.s.Rows(
		ctx,
		"list full syncs",
		"SELECT id, "+fullSyncCols+" FROM full_sync_runs WHERE "+cond+dialect.OrderBy(
			[]string{"id"},
			true,
		)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			run, err := scanFullSync(r)
			out = append(out, run)
			return err
		},
	)
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(
		out,
		p.Limit,
		func(r model.FullSyncRun) []string { return []string{dialect.Itoa(r.ID)} },
	)
	return page, next, nil
}

// Platform returns the platform federation store: access federation
// targets, platform federations and topologies (decision 0025).
func (s *Store) Platform() port.PlatformStore { return platformStore{s} }

type platformStore struct{ s *Store }

const accessTargetCols = "name, peer, active, created_at, entities, permission_filters, group_filters"

func scanAccessTarget(r dialect.PeersScanner) (model.AccessFederationTarget, error) {
	var (
		t                       model.AccessFederationTarget
		created                 sql.NullInt64
		entities, perms, groups sql.NullString
	)
	if err := r.Scan(
		&t.Name,
		&t.PeerInstanceID,
		&t.Active,
		&created,
		&entities,
		&perms,
		&groups,
	); err != nil {
		return t, err //nolint:wrapcheck // wrapped by the caller
	}
	t.Created = dialect.FromMicros(created)
	for _, d := range []struct {
		doc  sql.NullString
		into any
	}{{entities, &t.Entities}, {perms, &t.PermissionFilters}, {groups, &t.GroupFilters}} {
		if err := dialect.FromJSON(d.doc, d.into); err != nil {
			return t, err
		}
	}
	return t, nil
}

func (p platformStore) PutAccessTarget(ctx context.Context, t model.AccessFederationTarget) error {
	_, err := p.s.Exec(
		ctx,
		"put access federation target "+t.Name,
		dialect.Upsert(
			"access_federation_targets",
			strings.Split(accessTargetCols, ", "),
			[]string{"name"},
		),
		t.Name,
		t.PeerInstanceID,
		t.Active,
		dialect.Micros(t.Created),
		peersDoc(t.Entities),
		peersDoc(t.PermissionFilters),
		peersDoc(t.GroupFilters),
	)
	return err
}

func (p platformStore) AccessTarget(
	ctx context.Context,
	name string,
) (model.AccessFederationTarget, error) {
	t, err := scanAccessTarget(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT "+accessTargetCols+" FROM access_federation_targets WHERE name = ?"), name))
	if err != nil {
		return model.AccessFederationTarget{}, p.s.Wrap("get access federation target "+name, err)
	}
	return t, nil
}

func (p platformStore) AccessTargets(ctx context.Context) ([]model.AccessFederationTarget, error) {
	var out []model.AccessFederationTarget
	err := p.s.Rows(ctx, "list access federation targets",
		"SELECT "+accessTargetCols+" FROM access_federation_targets ORDER BY name", nil,
		func(r *sql.Rows) error {
			t, err := scanAccessTarget(r)
			out = append(out, t)
			return err
		})
	return out, err
}

func (p platformStore) DeleteAccessTarget(ctx context.Context, name string) error {
	return p.s.ExecOne(ctx, "delete access federation target "+name,
		"DELETE FROM access_federation_targets WHERE name = ?", name)
}

const platformFedCols = "id, name, description, connection_groups, entities"

func scanPlatformFed(r dialect.PeersScanner) (model.PlatformFederation, error) {
	var (
		f              model.PlatformFederation
		groups, entity sql.NullString
	)
	if err := r.Scan(&f.ID, &f.Name, &f.Description, &groups, &entity); err != nil {
		return f, err //nolint:wrapcheck // wrapped by the caller
	}
	if err := dialect.FromJSON(groups, &f.ConnectionGroups); err != nil {
		return f, err
	}
	return f, dialect.FromJSON(entity, &f.Entities)
}

func (p platformStore) PutFederation(ctx context.Context, f model.PlatformFederation) error {
	_, err := p.s.Exec(
		ctx,
		"put platform federation "+f.ID,
		dialect.Upsert(
			"platform_federations",
			strings.Split(platformFedCols, ", "),
			[]string{"id"},
		),
		f.ID,
		f.Name,
		f.Description,
		peersDoc(f.ConnectionGroups),
		peersDoc(f.Entities),
	)
	return err
}

func (p platformStore) Federation(
	ctx context.Context,
	id string,
) (model.PlatformFederation, error) {
	f, err := scanPlatformFed(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT "+platformFedCols+" FROM platform_federations WHERE id = ?"), id))
	if err != nil {
		return model.PlatformFederation{}, p.s.Wrap("get platform federation "+id, err)
	}
	return f, nil
}

func (p platformStore) Federations(ctx context.Context) ([]model.PlatformFederation, error) {
	var out []model.PlatformFederation
	err := p.s.Rows(ctx, "list platform federations",
		"SELECT "+platformFedCols+" FROM platform_federations ORDER BY id", nil,
		func(r *sql.Rows) error {
			f, err := scanPlatformFed(r)
			out = append(out, f)
			return err
		})
	return out, err
}

func (p platformStore) DeleteFederation(ctx context.Context, id string) error {
	return p.s.ExecOne(
		ctx,
		"delete platform federation "+id,
		"DELETE FROM platform_federations WHERE id = ?",
		id,
	)
}

const topologyCols = "key, name, description, created_at, modified_at"

func (p platformStore) PutTopology(ctx context.Context, t model.Topology) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.Exec(ctx, "put topology "+t.Key,
			dialect.Upsert("topologies", strings.Split(topologyCols, ", "), []string{"key"}),
			t.Key, t.Name, t.Description, dialect.Micros(t.Created),
			dialect.Micros(t.Modified)); err != nil {
			return err
		}
		if _, err := p.s.Exec(ctx, "put topology sites",
			"DELETE FROM topology_sites WHERE topology_key = ?", t.Key); err != nil {
			return err
		}
		for _, site := range t.Sites {
			if _, err := p.s.Exec(ctx, "put topology site "+site.SiteName,
				"INSERT INTO topology_sites (topology_key, site_name, peer) VALUES (?, ?, ?)",
				t.Key, site.SiteName, site.PeerInstanceID); err != nil {
				return err
			}
		}
		return nil
	})
}

// topologies reads topologies matching where with their sites.
func (p platformStore) topologies(
	ctx context.Context,
	where string,
	args ...any,
) ([]model.Topology, error) {
	var out []model.Topology
	err := p.s.Rows(ctx, "list topologies",
		"SELECT "+topologyCols+" FROM topologies"+where+" ORDER BY key", args,
		func(r *sql.Rows) error {
			var (
				t                 model.Topology
				created, modified sql.NullInt64
			)
			err := r.Scan(&t.Key, &t.Name, &t.Description, &created, &modified)
			t.Created, t.Modified = dialect.FromMicros(created), dialect.FromMicros(modified)
			out = append(out, t)
			return err //nolint:wrapcheck // wrapped by Rows
		})
	if err != nil || len(out) == 0 {
		return out, err
	}
	sites := map[string][]model.TopologySite{}
	siteWhere := strings.ReplaceAll(where, "key", "topology_key")
	err = p.s.Rows(
		ctx,
		"list topology sites",
		"SELECT topology_key, site_name, peer FROM topology_sites"+siteWhere+" ORDER BY topology_key, site_name",
		args,
		func(r *sql.Rows) error {
			var (
				key  string
				site model.TopologySite
			)
			err := r.Scan(&key, &site.SiteName, &site.PeerInstanceID)
			sites[key] = append(sites[key], site)
			return err //nolint:wrapcheck // wrapped by Rows
		},
	)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Sites = sites[out[i].Key]
	}
	return out, nil
}

func (p platformStore) Topology(ctx context.Context, key string) (model.Topology, error) {
	out, err := p.topologies(ctx, " WHERE key = ?", key)
	if err != nil {
		return model.Topology{}, err
	}
	if len(out) == 0 {
		return model.Topology{}, fmt.Errorf("get topology %s: %w", key, kernel.ErrNotFound)
	}
	return out[0], nil
}

func (p platformStore) Topologies(ctx context.Context) ([]model.Topology, error) {
	return p.topologies(ctx, "")
}

func (p platformStore) DeleteTopology(ctx context.Context, key string) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.Exec(ctx, "delete topology sites",
			"DELETE FROM topology_sites WHERE topology_key = ?", key); err != nil {
			return err
		}
		return p.s.ExecOne(ctx, "delete topology "+key, "DELETE FROM topologies WHERE key = ?", key)
	})
}
