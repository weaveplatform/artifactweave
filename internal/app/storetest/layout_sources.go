package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// errLayoutRollback rolls back the transaction that deletes a layout.
var errLayoutRollback = errors.New("rollback layout mutation")

func testRemoteLayoutFences(t *testing.T, s Store) {
	ctx, canonical, key := remoteFixture(t, s)
	canonical.RepoLayoutRef = "mapped-layout"
	storekit.Must(t, s.Repos().Update(ctx, canonical))
	upstream := canonical
	upstream.Key, upstream.RepoLayoutRef, upstream.RemoteRepoLayoutRef = "upstream-layout", "simple-default", "mapped-layout"
	storekit.Must(t, s.Repos().Create(ctx, upstream))
	other := canonical
	other.Key, other.RepoLayoutRef = "other-layout", "simple-default"
	storekit.Must(t, s.Repos().Create(ctx, other))
	local := canonical
	local.Key, local.Class = "local-layout", repo.Local
	storekit.Must(t, s.Repos().Create(ctx, local))
	states := map[string]model.SourceIdentity{}
	for _, c := range []repo.Config{canonical, upstream, other, local} {
		states[c.Key] = storekit.Get[model.SourceIdentity](t)(s.RemoteCache().Source(ctx, c.Key))
	}
	pending := storekit.Get[model.RemoteCacheToken](t)(s.RemoteCache().Begin(ctx, key))
	doc := configdoc.ConfigDocument{
		Kind: configdoc.ConfigLayout,
		Key:  "mapped-layout",
		Document: json.RawMessage(
			`{"artifactPathPattern":"[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`,
		),
	}
	storekit.Must(t, s.Configs().Put(ctx, doc))
	for _, c := range []repo.Config{canonical, upstream, other, local} {
		current := storekit.Get[model.SourceIdentity](t)(s.RemoteCache().Source(ctx, c.Key))
		previous := states[c.Key]
		changed := c.Key == canonical.Key || c.Key == upstream.Key
		if current.Repository != previous.Repository ||
			(current.Generation != previous.Generation) != changed {
			t.Fatal("layout creation fenced wrong source", c.Key, previous, current)
		}
		states[c.Key] = current
	}
	storekit.WantErr(t, s.RemoteCache().Check(ctx, pending), kernel.ErrConflict)
	doc.Updated = storekit.At(2)
	storekit.Must(t, s.Configs().Put(ctx, doc))
	storekit.Same(
		t,
		"unchanged layout",
		states[canonical.Key],
		storekit.Get[model.SourceIdentity](t)(s.RemoteCache().Source(ctx, canonical.Key)),
	)
	state := storekit.Get[model.RemoteSourceState](t)(s.RemoteCache().State(ctx, canonical.Key))
	state.ConnectionFailures, state.OfflineUntil, state.RetryAfterUntil = 10, storekit.At(
		5,
	), storekit.At(
		6,
	)
	storekit.Must(t, s.RemoteCache().PutState(ctx, canonical.Key, state))
	pending = storekit.Get[model.RemoteCacheToken](t)(s.RemoteCache().Begin(ctx, key))
	doc.Document = json.RawMessage(
		`{"artifactPathPattern":"different/[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`,
	)
	storekit.Must(t, s.Configs().Put(ctx, doc))
	storekit.WantErr(t, s.RemoteCache().Check(ctx, pending), kernel.ErrConflict)
	state = storekit.Get[model.RemoteSourceState](t)(s.RemoteCache().State(ctx, canonical.Key))
	if state.ConnectionFailures != 0 || !state.OfflineUntil.IsZero() ||
		!state.RetryAfterUntil.IsZero() {
		t.Fatal("layout edit retained old availability", state)
	}
	err := s.Tx(ctx, func(ctx context.Context) error {
		if err := s.Configs().Delete(ctx, doc.Kind, doc.Key); err != nil {
			return err //nolint:wrapcheck // the transaction returns the store's own error
		}
		return errLayoutRollback
	})
	storekit.WantErr(t, err, errLayoutRollback)
	storekit.Same(
		t,
		"rolled-back source",
		state,
		storekit.Get[model.RemoteSourceState](t)(s.RemoteCache().State(ctx, canonical.Key)),
	)
	storekit.Same(
		t,
		"rolled-back document",
		doc,
		storekit.Get[configdoc.ConfigDocument](t)(s.Configs().Get(ctx, doc.Kind, doc.Key)),
	)
	pending = storekit.Get[model.RemoteCacheToken](t)(s.RemoteCache().Begin(ctx, key))
	storekit.Must(t, s.Configs().Delete(ctx, doc.Kind, doc.Key))
	storekit.WantErr(t, s.RemoteCache().Check(ctx, pending), kernel.ErrConflict)
	storekit.Same(
		t,
		"unrelated source",
		states[other.Key],
		storekit.Get[model.SourceIdentity](t)(s.RemoteCache().Source(ctx, other.Key)),
	)
}

// remoteFixture creates the remote repository a test starts from and returns a
// cache key in it. The artifacts contract has the same fixture for its remote
// cache tests; this copy serves the layout fence test, which needs a whole
// composite.
func remoteFixture(
	t *testing.T,
	s Store,
) (context.Context, repo.Config, model.RemoteCacheKey) {
	t.Helper()
	ctx := context.Background()
	c := repo.Config{
		Key:         "upstream",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://source.example",
	}
	storekit.Must(t, s.Repos().Create(ctx, c))
	return ctx, c, model.RemoteCacheKey{
		RepoKey:          c.Key,
		Path:             "pkg/index.json",
		Variant:          "json",
		SettingsRevision: "secret-revision-1",
	}
}
