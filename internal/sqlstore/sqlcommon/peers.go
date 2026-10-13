package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// purposeInstanceKey seals the instance's Ed25519 private key; the row id is
// the instance id, so a key copied to another identity fails to open.
const purposeInstanceKey = "instance.private_key"

// peersScanner is a *sql.Row or *sql.Rows.
type peersScanner interface{ Scan(dest ...any) error }

// Instance implements store.Store.
func (s *Store) Instance() store.InstanceStore { return instanceStore{s} }

type instanceStore struct{ s *Store }

const instanceCols = "instance_id, public_key, private_key, namespace_id, protocol_version"

// args returns the column values of id with its private key sealed.
func (i instanceStore) args(id types.InstanceIdentity) ([]any, error) {
	sealed, err := i.s.seal(purposeInstanceKey, id.InstanceID, string(id.PrivateKey))
	if err != nil {
		return nil, err
	}
	return []any{
		id.InstanceID,
		[]byte(id.PublicKey),
		sealed,
		id.NamespaceID,
		id.ProtocolVersion,
	}, nil
}

func (i instanceStore) Init(ctx context.Context, id types.InstanceIdentity) error {
	args, err := i.args(id)
	if err != nil {
		return err
	}
	_, err = i.s.exec(ctx, "init instance",
		"INSERT INTO instance (id, "+instanceCols+") VALUES (1, ?, ?, ?, ?, ?)", args...)
	return err
}

func (i instanceStore) Get(ctx context.Context) (types.InstanceIdentity, error) {
	var (
		out         types.InstanceIdentity
		pub, sealed []byte
	)
	err := i.s.q(ctx).QueryRowContext(ctx, "SELECT "+instanceCols+" FROM instance WHERE id = 1").
		Scan(&out.InstanceID, &pub, &sealed, &out.NamespaceID, &out.ProtocolVersion)
	if err != nil {
		return out, i.s.wrap("get instance", err)
	}
	if len(pub) > 0 {
		out.PublicKey = pub
	}
	priv, err := i.s.unseal(purposeInstanceKey, out.InstanceID, sealed)
	if err != nil {
		return types.InstanceIdentity{}, err
	}
	if priv != "" {
		out.PrivateKey = []byte(priv)
	}
	return out, nil
}

func (i instanceStore) Update(ctx context.Context, id types.InstanceIdentity) error {
	args, err := i.args(id)
	if err != nil {
		return err
	}
	return i.s.execOne(
		ctx,
		"update instance",
		"UPDATE instance SET instance_id = ?, public_key = ?, "+
			"private_key = ?, namespace_id = ?, protocol_version = ? WHERE id = 1",
		args...)
}

// Peers implements store.Store.
func (s *Store) Peers() store.PeerStore { return peerStore{s} }

type peerStore struct{ s *Store }

const peerCols = "instance_id, base_url, public_key, protocol_version, status, last_seen_at, " +
	"display_name, city_name, country_code, edge_role"

func scanPeer(r peersScanner) (types.Peer, error) {
	var (
		p    types.Peer
		pub  []byte
		seen sql.NullInt64
	)
	err := r.Scan(&p.InstanceID, &p.BaseURL, &pub, &p.ProtocolVersion, &p.Status, &seen,
		&p.DisplayName, &p.CityName, &p.CountryCode, &p.EdgeRole)
	if len(pub) > 0 {
		p.PublicKey = pub
	}
	p.LastSeen = fromMicros(seen)
	return p, err //nolint:wrapcheck // wrapped by the caller
}

func (p peerStore) Put(ctx context.Context, peer types.Peer) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.exec(
			ctx,
			"put peer "+peer.InstanceID,
			Upsert("peers", strings.Split(peerCols, ", "), []string{"instance_id"}),
			peer.InstanceID,
			peer.BaseURL,
			[]byte(peer.PublicKey),
			peer.ProtocolVersion,
			string(peer.Status),
			micros(peer.LastSeen),
			peer.DisplayName,
			peer.CityName,
			peer.CountryCode,
			peer.EdgeRole,
		); err != nil {
			return err
		}
		if _, err := p.s.exec(ctx, "put peer use cases",
			"DELETE FROM peer_use_cases WHERE peer = ?", peer.InstanceID); err != nil {
			return err
		}
		for _, u := range peerUseCaseSet(peer.UseCases) {
			if _, err := p.s.exec(
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
func peerUseCaseSet(in []types.UseCase) []types.UseCase {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func (p peerStore) Get(ctx context.Context, instanceID string) (types.Peer, error) {
	peer, err := scanPeer(p.s.q(ctx).QueryRowContext(ctx,
		p.s.d.Rebind("SELECT "+peerCols+" FROM peers WHERE instance_id = ?"), instanceID))
	if err != nil {
		return types.Peer{}, p.s.wrap("get peer "+instanceID, err)
	}
	uses, err := p.useCases(ctx, "WHERE peer = ?", instanceID)
	if err != nil {
		return types.Peer{}, err
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
) (map[string][]types.UseCase, error) {
	out := map[string][]types.UseCase{}
	err := p.s.rows(ctx, "list peer use cases",
		"SELECT peer, use_case FROM peer_use_cases "+where+" ORDER BY peer, use_case", args,
		func(r *sql.Rows) error {
			var peer, use string
			if err := r.Scan(&peer, &use); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			out[peer] = append(out[peer], types.UseCase(use))
			return nil
		})
	return out, err
}

func (p peerStore) List(ctx context.Context, f store.PeerFilter) ([]types.Peer, error) {
	where, args := "", []any(nil)
	if f.UseCase != "" {
		where = " WHERE instance_id IN (SELECT peer FROM peer_use_cases WHERE use_case = ?)"
		args = []any{string(f.UseCase)}
	}
	var out []types.Peer
	err := p.s.rows(
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
		if _, err := p.s.exec(ctx, "delete peer use cases",
			"DELETE FROM peer_use_cases WHERE peer = ?", instanceID); err != nil {
			return err
		}
		return p.s.execOne(
			ctx,
			"delete peer "+instanceID,
			"DELETE FROM peers WHERE instance_id = ?",
			instanceID,
		)
	})
}

func (p peerStore) AddNonce(ctx context.Context, n types.PairingNonce) error {
	_, err := p.s.exec(
		ctx,
		"add pairing nonce",
		"INSERT INTO pairing_nonces (nonce_hash, use_case, expires_at, redeemed_at) VALUES (?, ?, ?, ?)",
		n.NonceHash,
		string(n.UseCase),
		micros(n.ExpiresAt),
		micros(n.RedeemedAt),
	)
	return err
}

func (p peerStore) RedeemNonce(
	ctx context.Context,
	hash []byte,
	now time.Time,
) (types.PairingNonce, error) {
	var out types.PairingNonce
	err := p.s.Tx(ctx, func(ctx context.Context) error {
		// The conditional update is the redemption: of two concurrent
		// redeemers exactly one changes the row.
		n, err := p.s.exec(
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
		err = p.s.q(ctx).QueryRowContext(ctx, p.s.d.Rebind(
			"SELECT use_case, expires_at, redeemed_at FROM pairing_nonces WHERE nonce_hash = ?",
		), hash).
			Scan(&use, &exp, &red)
		if err != nil {
			return p.s.wrap(fmt.Sprintf("redeem pairing nonce %x", hash), err)
		}
		out = types.PairingNonce{
			NonceHash: hash, UseCase: types.UseCase(use),
			ExpiresAt: fromMicros(exp), RedeemedAt: fromMicros(red),
		}
		switch {
		case n == 1:
			return nil
		case red.Valid:
			return fmt.Errorf("pairing nonce %x already redeemed: %w", hash, store.ErrConflict)
		default:
			return fmt.Errorf("pairing nonce %x expired: %w", hash, store.ErrNotFound)
		}
	})
	if err != nil {
		return types.PairingNonce{}, err
	}
	return out, nil
}

func (p peerStore) PruneNonces(ctx context.Context, before time.Time) (int64, error) {
	return p.s.exec(ctx, "prune pairing nonces",
		"DELETE FROM pairing_nonces WHERE expires_at IS NULL OR expires_at < ?", before.UnixMicro())
}

// Outbox implements store.Store.
func (s *Store) Outbox() store.OutboxStore { return outboxStore{s} }

type outboxStore struct{ s *Store }

const outboxCols = "origin, stream, seq, hlc_wall, hlc_logical, event_type, payload, blob_sha256, blob_size, created_at"

func scanOutbox(r peersScanner) (types.OutboxEntry, error) {
	var (
		e       types.OutboxEntry
		logical int64
		created sql.NullInt64
	)
	err := r.Scan(&e.Origin, &e.Stream, &e.Seq, &e.Time.Wall, &logical, &e.EventType, &e.Payload,
		&e.BlobSHA256, &e.BlobSize, &created)
	e.Time.Logical = uint32(logical) // #nosec G115 -- written from a uint32
	e.Created = fromMicros(created)
	if len(e.Payload) == 0 {
		e.Payload = nil
	}
	return e, err //nolint:wrapcheck // wrapped by the caller
}

func (o outboxStore) Append(ctx context.Context, e types.OutboxEntry) (types.OutboxEntry, error) {
	err := o.s.Tx(ctx, func(ctx context.Context) error {
		// The sequence row serialises appenders: the UPDATE holds its lock
		// until commit, so two transactions never draw the same number and a
		// rolled-back append returns its number with it.
		if _, err := o.s.exec(ctx, "append outbox", InsertIgnore("outbox_sequences",
			[]string{"origin", "stream", "last_seq"}, []string{"origin", "stream"}),
			e.Origin, e.Stream, 0); err != nil {
			return err
		}
		if err := o.s.q(ctx).
			QueryRowContext(ctx, o.s.d.Rebind("UPDATE outbox_sequences SET last_seq = last_seq + 1 "+
				"WHERE origin = ? AND stream = ? RETURNING last_seq"), e.Origin, e.Stream).
			Scan(&e.Seq); err != nil {
			return o.s.wrap("append outbox", err)
		}
		_, err := o.s.exec(
			ctx,
			"append outbox",
			Insert("outbox", strings.Split(outboxCols, ", ")),
			e.Origin,
			e.Stream,
			e.Seq,
			e.Time.Wall,
			int64(e.Time.Logical),
			e.EventType,
			peersBytes(e.Payload),
			e.BlobSHA256,
			e.BlobSize,
			micros(e.Created),
		)
		return err
	})
	if err != nil {
		return types.OutboxEntry{}, err
	}
	return store.Normalise(e), nil
}

// peersBytes stores an empty byte slice as NULL.
func peersBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (o outboxStore) Read(
	ctx context.Context, origin, stream string, after int64, limit int,
) ([]types.OutboxEntry, error) {
	var out []types.OutboxEntry
	err := o.s.rows(ctx, "read outbox", "SELECT "+outboxCols+" FROM outbox "+
		"WHERE origin = ? AND stream = ? AND seq > ? ORDER BY seq LIMIT ?",
		[]any{origin, stream, after, limitOr(limit)},
		func(r *sql.Rows) error {
			e, err := scanOutbox(r)
			out = append(out, e)
			return err
		})
	return out, err
}

func (o outboxStore) LastSeq(ctx context.Context, origin, stream string) (int64, error) {
	var seq int64
	err := o.s.q(ctx).QueryRowContext(ctx, o.s.d.Rebind(
		"SELECT COALESCE(MAX(last_seq), 0) FROM outbox_sequences WHERE origin = ? AND stream = ?"),
		origin, stream).Scan(&seq)
	return seq, o.s.wrap("outbox last sequence", err)
}

func (o outboxStore) Streams(ctx context.Context, origin string) ([]string, error) {
	var out []string
	err := o.s.rows(ctx, "list outbox streams",
		"SELECT stream FROM outbox_sequences WHERE origin = ? ORDER BY stream", []any{origin},
		func(r *sql.Rows) error {
			var stream string
			err := r.Scan(&stream)
			out = append(out, stream)
			return err //nolint:wrapcheck // wrapped by rows
		})
	return out, err
}

func (o outboxStore) Prune(ctx context.Context, origin, stream string, upTo int64) (int64, error) {
	return o.s.exec(ctx, "prune outbox",
		"DELETE FROM outbox WHERE origin = ? AND stream = ? AND seq <= ?", origin, stream, upTo)
}

const peerCursorCols = "peer, stream, acked_seq, last_sent_at, last_error"

func scanPeerCursor(r peersScanner) (types.PeerCursor, error) {
	var (
		c    types.PeerCursor
		sent sql.NullInt64
	)
	err := r.Scan(&c.PeerInstanceID, &c.Stream, &c.AckedSeq, &sent, &c.LastError)
	c.LastSent = fromMicros(sent)
	return c, err //nolint:wrapcheck // wrapped by the caller
}

func (o outboxStore) PutCursor(ctx context.Context, c types.PeerCursor) error {
	_, err := o.s.exec(ctx, "put peer cursor",
		Upsert("peer_cursors", strings.Split(peerCursorCols, ", "), []string{"peer", "stream"}),
		c.PeerInstanceID, c.Stream, c.AckedSeq, micros(c.LastSent), c.LastError)
	return err
}

func (o outboxStore) Cursor(ctx context.Context, peer, stream string) (types.PeerCursor, error) {
	c, err := scanPeerCursor(o.s.q(ctx).QueryRowContext(ctx, o.s.d.Rebind(
		"SELECT "+peerCursorCols+" FROM peer_cursors WHERE peer = ? AND stream = ?"), peer, stream))
	if err != nil {
		return types.PeerCursor{}, o.s.wrap("get peer cursor "+peer+" "+stream, err)
	}
	return c, nil
}

func (o outboxStore) Cursors(ctx context.Context, peer string) ([]types.PeerCursor, error) {
	where, args := "", []any(nil)
	if peer != "" {
		where, args = " WHERE peer = ?", []any{peer}
	}
	var out []types.PeerCursor
	err := o.s.rows(ctx, "list peer cursors",
		"SELECT "+peerCursorCols+" FROM peer_cursors"+where+" ORDER BY peer, stream", args,
		func(r *sql.Rows) error {
			c, err := scanPeerCursor(r)
			out = append(out, c)
			return err
		})
	return out, err
}

func (o outboxStore) DeleteCursors(ctx context.Context, peer string) error {
	_, err := o.s.exec(ctx, "delete peer cursors", "DELETE FROM peer_cursors WHERE peer = ?", peer)
	return err
}

func (o outboxStore) Applied(
	ctx context.Context,
	origin, stream string,
) (types.AppliedMarker, error) {
	m := types.AppliedMarker{Origin: origin, Stream: stream}
	err := o.s.q(ctx).QueryRowContext(ctx, o.s.d.Rebind(
		"SELECT COALESCE(MAX(last_seq), 0) FROM applied_markers WHERE origin = ? AND stream = ?"),
		origin, stream).Scan(&m.LastSeq)
	return m, o.s.wrap("get applied marker", err)
}

func (o outboxStore) SetApplied(ctx context.Context, m types.AppliedMarker) error {
	_, err := o.s.exec(
		ctx,
		"set applied marker",
		Upsert(
			"applied_markers",
			[]string{"origin", "stream", "last_seq"},
			[]string{"origin", "stream"},
		),
		m.Origin,
		m.Stream,
		m.LastSeq,
	)
	return err
}

const failedFetchCols = "stream, sha256, sha1, repo_key, first_failed_at, failures, last_error"

func scanFailedFetch(r peersScanner) (types.FailedBlobFetch, error) {
	var (
		f     types.FailedBlobFetch
		first sql.NullInt64
	)
	err := r.Scan(&f.Stream, &f.SHA256, &f.SHA1, &f.RepoKey, &first, &f.Failures, &f.LastError)
	f.FirstFailed = fromMicros(first)
	return f, err //nolint:wrapcheck // wrapped by the caller
}

func (o outboxStore) RecordFailedFetch(
	ctx context.Context,
	f types.FailedBlobFetch,
) (types.FailedBlobFetch, error) {
	var out types.FailedBlobFetch
	err := o.s.Tx(ctx, func(ctx context.Context) error {
		// Insert-if-absent with no failures, then count this one, so two
		// first failures racing on PostgreSQL both land on one row.
		if _, err := o.s.exec(
			ctx,
			"record failed fetch",
			InsertIgnore(
				"failed_blob_fetches",
				strings.Split(failedFetchCols, ", "),
				[]string{"stream", "sha256"},
			),
			f.Stream,
			f.SHA256,
			f.SHA1,
			f.RepoKey,
			micros(f.FirstFailed),
			0,
			f.LastError,
		); err != nil {
			return err
		}
		if err := o.s.execOne(ctx, "record failed fetch", "UPDATE failed_blob_fetches "+
			"SET failures = failures + 1, last_error = ? WHERE stream = ? AND sha256 = ?",
			f.LastError, f.Stream, f.SHA256); err != nil {
			return err
		}
		var err error
		out, err = scanFailedFetch(o.s.q(ctx).QueryRowContext(ctx, o.s.d.Rebind(
			"SELECT "+failedFetchCols+" FROM failed_blob_fetches WHERE stream = ? AND sha256 = ?",
		), f.Stream, f.SHA256))
		return o.s.wrap("record failed fetch", err)
	})
	if err != nil {
		return types.FailedBlobFetch{}, err
	}
	return out, nil
}

func (o outboxStore) FailedFetches(
	ctx context.Context,
	stream string,
) ([]types.FailedBlobFetch, error) {
	where, args := "", []any(nil)
	if stream != "" {
		where, args = " WHERE stream = ?", []any{stream}
	}
	var out []types.FailedBlobFetch
	err := o.s.rows(
		ctx,
		"list failed fetches",
		"SELECT "+failedFetchCols+" FROM failed_blob_fetches"+where+" ORDER BY stream, sha256",
		args,
		func(r *sql.Rows) error {
			f, err := scanFailedFetch(r)
			out = append(out, f)
			return err
		},
	)
	return out, err
}

func (o outboxStore) ClearFailedFetch(ctx context.Context, stream, sha256 string) error {
	return o.s.execOne(ctx, "clear failed fetch "+stream+" "+sha256,
		"DELETE FROM failed_blob_fetches WHERE stream = ? AND sha256 = ?", stream, sha256)
}

// Federation implements store.Store.
func (s *Store) Federation() store.FederationStore { return federationStore{s} }

type federationStore struct{ s *Store }

const fedMemberCols = "repo_key, peer, base_url, remote_repo_key, enabled"

func (f federationStore) SetMembers(
	ctx context.Context,
	repoKey string,
	members []types.FederationMember,
) error {
	for _, m := range members {
		if m.PeerInstanceID == "" {
			return fmt.Errorf(
				"federation member %q of %s has no peer: %w",
				m.BaseURL,
				repoKey,
				store.ErrInvalid,
			)
		}
	}
	return f.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := f.s.exec(ctx, "set federation members",
			"DELETE FROM federation_members WHERE repo_key = ?", repoKey); err != nil {
			return err
		}
		for _, m := range members {
			if _, err := f.s.exec(
				ctx,
				"set federation members",
				Insert("federation_members", strings.Split(fedMemberCols, ", ")),
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
) ([]types.FederationMember, error) {
	var out []types.FederationMember
	err := f.s.rows(
		ctx,
		"list federation members",
		"SELECT "+fedMemberCols+" FROM federation_members WHERE "+where+" = ? ORDER BY "+order,
		[]any{arg},
		func(r *sql.Rows) error {
			var m types.FederationMember
			err := r.Scan(&m.RepoKey, &m.PeerInstanceID, &m.BaseURL, &m.RemoteRepoKey, &m.Enabled)
			out = append(out, m)
			return err //nolint:wrapcheck // wrapped by rows
		},
	)
	return out, err
}

func (f federationStore) Members(
	ctx context.Context,
	repoKey string,
) ([]types.FederationMember, error) {
	return f.members(ctx, "repo_key", "peer", repoKey)
}

func (f federationStore) MembersWithPeer(
	ctx context.Context,
	peer string,
) ([]types.FederationMember, error) {
	return f.members(ctx, "peer", "repo_key", peer)
}

const fullSyncCols = "repo_key, peer, forced, snapshot_seq, started_at, finished_at, status, result_paths"

func scanFullSync(r peersScanner) (types.FullSyncRun, error) {
	var (
		run               types.FullSyncRun
		started, finished sql.NullInt64
		status            string
		paths             sql.NullString
	)
	if err := r.Scan(&run.ID, &run.RepoKey, &run.PeerInstanceID, &run.Forced, &run.SnapshotSeq,
		&started, &finished, &status, &paths); err != nil {
		return run, err //nolint:wrapcheck // wrapped by the caller
	}
	run.Started, run.Finished, run.Status = fromMicros(
		started,
	), fromMicros(
		finished,
	), types.RunStatus(
		status,
	)
	return run, fromJSON(paths, &run.ResultPaths)
}

// fullSyncArgs returns the column values of r after the id.
func fullSyncArgs(r types.FullSyncRun) []any {
	return []any{
		r.RepoKey, r.PeerInstanceID, r.Forced, r.SnapshotSeq,
		micros(r.Started), micros(r.Finished), string(r.Status), peersDoc(r.ResultPaths),
	}
}

// peersDoc encodes a document column of this group. Every document here is a
// string slice, a map of string slices or a types.PatternFilter, none of
// which can fail to marshal, so the error toJSON reports for other types
// cannot occur.
func peersDoc(v any) any {
	doc, _ := toJSON(v)
	return doc
}

func (f federationStore) CreateFullSync(
	ctx context.Context,
	r types.FullSyncRun,
) (types.FullSyncRun, error) {
	var err error
	if r.ID, err = f.s.insertID(
		ctx,
		"create full sync",
		Insert(
			"full_sync_runs",
			strings.Split(fullSyncCols, ", "),
		),
		fullSyncArgs(r)...); err != nil {
		return types.FullSyncRun{}, err
	}
	return store.Normalise(r), nil
}

func (f federationStore) FullSync(ctx context.Context, id int64) (types.FullSyncRun, error) {
	r, err := scanFullSync(f.s.q(ctx).QueryRowContext(ctx, f.s.d.Rebind(
		"SELECT id, "+fullSyncCols+" FROM full_sync_runs WHERE id = ?"), id))
	if err != nil {
		return types.FullSyncRun{}, f.s.wrap(fmt.Sprintf("get full sync %d", id), err)
	}
	return r, nil
}

func (f federationStore) UpdateFullSync(ctx context.Context, r types.FullSyncRun) error {
	return f.s.execOne(ctx, fmt.Sprintf("update full sync %d", r.ID), "UPDATE full_sync_runs SET "+
		"repo_key = ?, peer = ?, forced = ?, snapshot_seq = ?, started_at = ?, finished_at = ?, "+
		"status = ?, result_paths = ? WHERE id = ?", append(fullSyncArgs(r), r.ID)...)
}

func (f federationStore) FullSyncs(
	ctx context.Context, repoKey string, p store.Page,
) ([]types.FullSyncRun, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, []string{"id"}, map[string]bool{"id": true}, true)
	if err != nil {
		return nil, "", err
	}
	if repoKey != "" {
		cond += " AND repo_key = ?"
		args = append(args, repoKey)
	}
	var out []types.FullSyncRun
	err = f.s.rows(
		ctx,
		"list full syncs",
		"SELECT id, "+fullSyncCols+" FROM full_sync_runs WHERE "+cond+orderBy(
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
	page, next := store.Trim(
		out,
		p.Limit,
		func(r types.FullSyncRun) []string { return []string{itoa(r.ID)} },
	)
	return page, next, nil
}

// Platform implements store.Store.
func (s *Store) Platform() store.PlatformStore { return platformStore{s} }

type platformStore struct{ s *Store }

const accessTargetCols = "name, peer, active, created_at, entities, permission_filters, group_filters"

func scanAccessTarget(r peersScanner) (types.AccessFederationTarget, error) {
	var (
		t                       types.AccessFederationTarget
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
	t.Created = fromMicros(created)
	for _, d := range []struct {
		doc  sql.NullString
		into any
	}{{entities, &t.Entities}, {perms, &t.PermissionFilters}, {groups, &t.GroupFilters}} {
		if err := fromJSON(d.doc, d.into); err != nil {
			return t, err
		}
	}
	return t, nil
}

func (p platformStore) PutAccessTarget(ctx context.Context, t types.AccessFederationTarget) error {
	_, err := p.s.exec(
		ctx,
		"put access federation target "+t.Name,
		Upsert(
			"access_federation_targets",
			strings.Split(accessTargetCols, ", "),
			[]string{"name"},
		),
		t.Name,
		t.PeerInstanceID,
		t.Active,
		micros(t.Created),
		peersDoc(t.Entities),
		peersDoc(t.PermissionFilters),
		peersDoc(t.GroupFilters),
	)
	return err
}

func (p platformStore) AccessTarget(
	ctx context.Context,
	name string,
) (types.AccessFederationTarget, error) {
	t, err := scanAccessTarget(p.s.q(ctx).QueryRowContext(ctx, p.s.d.Rebind(
		"SELECT "+accessTargetCols+" FROM access_federation_targets WHERE name = ?"), name))
	if err != nil {
		return types.AccessFederationTarget{}, p.s.wrap("get access federation target "+name, err)
	}
	return t, nil
}

func (p platformStore) AccessTargets(ctx context.Context) ([]types.AccessFederationTarget, error) {
	var out []types.AccessFederationTarget
	err := p.s.rows(ctx, "list access federation targets",
		"SELECT "+accessTargetCols+" FROM access_federation_targets ORDER BY name", nil,
		func(r *sql.Rows) error {
			t, err := scanAccessTarget(r)
			out = append(out, t)
			return err
		})
	return out, err
}

func (p platformStore) DeleteAccessTarget(ctx context.Context, name string) error {
	return p.s.execOne(ctx, "delete access federation target "+name,
		"DELETE FROM access_federation_targets WHERE name = ?", name)
}

const platformFedCols = "id, name, description, connection_groups, entities"

func scanPlatformFed(r peersScanner) (types.PlatformFederation, error) {
	var (
		f              types.PlatformFederation
		groups, entity sql.NullString
	)
	if err := r.Scan(&f.ID, &f.Name, &f.Description, &groups, &entity); err != nil {
		return f, err //nolint:wrapcheck // wrapped by the caller
	}
	if err := fromJSON(groups, &f.ConnectionGroups); err != nil {
		return f, err
	}
	return f, fromJSON(entity, &f.Entities)
}

func (p platformStore) PutFederation(ctx context.Context, f types.PlatformFederation) error {
	_, err := p.s.exec(ctx, "put platform federation "+f.ID,
		Upsert("platform_federations", strings.Split(platformFedCols, ", "), []string{"id"}),
		f.ID, f.Name, f.Description, peersDoc(f.ConnectionGroups), peersDoc(f.Entities))
	return err
}

func (p platformStore) Federation(
	ctx context.Context,
	id string,
) (types.PlatformFederation, error) {
	f, err := scanPlatformFed(p.s.q(ctx).QueryRowContext(ctx, p.s.d.Rebind(
		"SELECT "+platformFedCols+" FROM platform_federations WHERE id = ?"), id))
	if err != nil {
		return types.PlatformFederation{}, p.s.wrap("get platform federation "+id, err)
	}
	return f, nil
}

func (p platformStore) Federations(ctx context.Context) ([]types.PlatformFederation, error) {
	var out []types.PlatformFederation
	err := p.s.rows(ctx, "list platform federations",
		"SELECT "+platformFedCols+" FROM platform_federations ORDER BY id", nil,
		func(r *sql.Rows) error {
			f, err := scanPlatformFed(r)
			out = append(out, f)
			return err
		})
	return out, err
}

func (p platformStore) DeleteFederation(ctx context.Context, id string) error {
	return p.s.execOne(
		ctx,
		"delete platform federation "+id,
		"DELETE FROM platform_federations WHERE id = ?",
		id,
	)
}

const topologyCols = "key, name, description, created_at, modified_at"

func (p platformStore) PutTopology(ctx context.Context, t types.Topology) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.exec(ctx, "put topology "+t.Key,
			Upsert("topologies", strings.Split(topologyCols, ", "), []string{"key"}),
			t.Key, t.Name, t.Description, micros(t.Created), micros(t.Modified)); err != nil {
			return err
		}
		if _, err := p.s.exec(ctx, "put topology sites",
			"DELETE FROM topology_sites WHERE topology_key = ?", t.Key); err != nil {
			return err
		}
		for _, site := range t.Sites {
			if _, err := p.s.exec(ctx, "put topology site "+site.SiteName,
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
) ([]types.Topology, error) {
	var out []types.Topology
	err := p.s.rows(ctx, "list topologies",
		"SELECT "+topologyCols+" FROM topologies"+where+" ORDER BY key", args,
		func(r *sql.Rows) error {
			var (
				t                 types.Topology
				created, modified sql.NullInt64
			)
			err := r.Scan(&t.Key, &t.Name, &t.Description, &created, &modified)
			t.Created, t.Modified = fromMicros(created), fromMicros(modified)
			out = append(out, t)
			return err //nolint:wrapcheck // wrapped by rows
		})
	if err != nil || len(out) == 0 {
		return out, err
	}
	sites := map[string][]types.TopologySite{}
	siteWhere := strings.ReplaceAll(where, "key", "topology_key")
	err = p.s.rows(
		ctx,
		"list topology sites",
		"SELECT topology_key, site_name, peer FROM topology_sites"+siteWhere+" ORDER BY topology_key, site_name",
		args,
		func(r *sql.Rows) error {
			var (
				key  string
				site types.TopologySite
			)
			err := r.Scan(&key, &site.SiteName, &site.PeerInstanceID)
			sites[key] = append(sites[key], site)
			return err //nolint:wrapcheck // wrapped by rows
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

func (p platformStore) Topology(ctx context.Context, key string) (types.Topology, error) {
	out, err := p.topologies(ctx, " WHERE key = ?", key)
	if err != nil {
		return types.Topology{}, err
	}
	if len(out) == 0 {
		return types.Topology{}, fmt.Errorf("get topology %s: %w", key, store.ErrNotFound)
	}
	return out[0], nil
}

func (p platformStore) Topologies(ctx context.Context) ([]types.Topology, error) {
	return p.topologies(ctx, "")
}

func (p platformStore) DeleteTopology(ctx context.Context, key string) error {
	return p.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := p.s.exec(ctx, "delete topology sites",
			"DELETE FROM topology_sites WHERE topology_key = ?", key); err != nil {
			return err
		}
		return p.s.execOne(ctx, "delete topology "+key, "DELETE FROM topologies WHERE key = ?", key)
	})
}
