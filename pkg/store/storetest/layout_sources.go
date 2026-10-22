package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func testRemoteLayoutFences(t *testing.T, s store.Store) {
	ctx, canonical, key := remoteFixture(t, s)
	canonical.RepoLayoutRef = "mapped-layout"
	must(t, s.Repos().Update(ctx, canonical))
	upstream := canonical
	upstream.Key, upstream.RepoLayoutRef, upstream.RemoteRepoLayoutRef = "upstream-layout", "simple-default", "mapped-layout"
	must(t, s.Repos().Create(ctx, upstream))
	other := canonical
	other.Key, other.RepoLayoutRef = "other-layout", "simple-default"
	must(t, s.Repos().Create(ctx, other))
	local := canonical
	local.Key, local.Class = "local-layout", repo.Local
	must(t, s.Repos().Create(ctx, local))
	states := map[string]types.SourceIdentity{}
	for _, c := range []repo.Config{canonical, upstream, other, local} {
		states[c.Key] = get[types.SourceIdentity](t)(s.RemoteCache().Source(ctx, c.Key))
	}
	pending := get[types.RemoteCacheToken](t)(s.RemoteCache().Begin(ctx, key))
	doc := types.ConfigDocument{
		Kind: types.ConfigLayout,
		Key:  "mapped-layout",
		Document: json.RawMessage(
			`{"artifactPathPattern":"[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`,
		),
	}
	must(t, s.Configs().Put(ctx, doc))
	for _, c := range []repo.Config{canonical, upstream, other, local} {
		current := get[types.SourceIdentity](t)(s.RemoteCache().Source(ctx, c.Key))
		previous := states[c.Key]
		changed := c.Key == canonical.Key || c.Key == upstream.Key
		if current.Repository != previous.Repository ||
			(current.Generation != previous.Generation) != changed {
			t.Fatal("layout creation fenced wrong source", c.Key, previous, current)
		}
		states[c.Key] = current
	}
	wantErr(t, s.RemoteCache().Check(ctx, pending), store.ErrConflict)
	doc.Updated = at(2)
	must(t, s.Configs().Put(ctx, doc))
	same(
		t,
		"unchanged layout",
		states[canonical.Key],
		get[types.SourceIdentity](t)(s.RemoteCache().Source(ctx, canonical.Key)),
	)
	state := get[types.RemoteSourceState](t)(s.RemoteCache().State(ctx, canonical.Key))
	state.ConnectionFailures, state.OfflineUntil, state.RetryAfterUntil = 10, at(5), at(6)
	must(t, s.RemoteCache().PutState(ctx, canonical.Key, state))
	pending = get[types.RemoteCacheToken](t)(s.RemoteCache().Begin(ctx, key))
	doc.Document = json.RawMessage(
		`{"artifactPathPattern":"different/[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`,
	)
	must(t, s.Configs().Put(ctx, doc))
	wantErr(t, s.RemoteCache().Check(ctx, pending), store.ErrConflict)
	state = get[types.RemoteSourceState](t)(s.RemoteCache().State(ctx, canonical.Key))
	if state.ConnectionFailures != 0 || !state.OfflineUntil.IsZero() ||
		!state.RetryAfterUntil.IsZero() {
		t.Fatal("layout edit retained old availability", state)
	}
	sentinel := errors.New("rollback layout mutation")
	err := s.Tx(ctx, func(ctx context.Context) error {
		if err := s.Configs().Delete(ctx, doc.Kind, doc.Key); err != nil {
			return err
		}
		return sentinel
	})
	wantErr(t, err, sentinel)
	same(
		t,
		"rolled-back source",
		state,
		get[types.RemoteSourceState](t)(s.RemoteCache().State(ctx, canonical.Key)),
	)
	same(
		t,
		"rolled-back document",
		doc,
		get[types.ConfigDocument](t)(s.Configs().Get(ctx, doc.Kind, doc.Key)),
	)
	pending = get[types.RemoteCacheToken](t)(s.RemoteCache().Begin(ctx, key))
	must(t, s.Configs().Delete(ctx, doc.Kind, doc.Key))
	wantErr(t, s.RemoteCache().Check(ctx, pending), store.ErrConflict)
	same(
		t,
		"unrelated source",
		states[other.Key],
		get[types.SourceIdentity](t)(s.RemoteCache().Source(ctx, other.Key)),
	)
}
