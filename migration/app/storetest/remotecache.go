package storetest

import (
	"encoding/json"
	"errors"
	"testing"

	appsql "github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The artifacts module's tests run the remote cache persistence suite on its
// SQL adapter alone. What stays here needs the whole metadata store: a layout
// document, which the platform's config store keeps and whose edit fences the
// remote sources that use it.

// RemoteLayoutPersistence checks that a layout edit fences the remote source
// of a repository that uses the layout, and that the layout and the new
// source generation survive reopening the backend together.
func RemoteLayoutPersistence(t *testing.T, s *appsql.Store, reopen func() *appsql.Store) {
	t.Helper()
	Reset(t, s.DB(), s.Dialect())
	if err := s.Repos().Create(t.Context(), repo.Config{
		Key:         "first",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		URL:         "https://upstream.example",
	}); err != nil {
		t.Fatal(err)
	}
	remoteLayoutPersistence(t, s, reopen)
}

func remoteLayoutPersistence(t *testing.T, s *appsql.Store, reopen func() *appsql.Store) {
	t.Helper()
	ctx := t.Context()
	c, err := s.Repos().Get(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	c.RepoLayoutRef = "durable-layout"
	if err = s.Repos().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	token, err := s.RemoteCache().
		Begin(ctx, types.RemoteCacheKey{RepoKey: c.Key, Path: "layout-pending", Variant: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	doc := types.ConfigDocument{
		Kind: types.ConfigLayout,
		Key:  c.RepoLayoutRef,
		Document: json.RawMessage(
			`{"artifactPathPattern":"[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`,
		),
	}
	if err = s.Configs().Put(ctx, doc); err != nil {
		t.Fatal(err)
	}
	current, err := s.RemoteCache().Source(ctx, c.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = reopen()
	got, err := s.RemoteCache().Source(ctx, c.Key)
	if err != nil || got != current {
		t.Fatal("restart lost layout source generation", got, current, err)
	}
	if err = s.RemoteCache().Check(ctx, token); !errors.Is(err, store.ErrConflict) {
		t.Fatal("restart restored stale layout publication authority", err)
	}
	stored, err := s.Configs().Get(ctx, doc.Kind, doc.Key)
	if err != nil || string(stored.Document) != string(doc.Document) {
		t.Fatal("layout and generation did not persist together", stored, err)
	}
}
