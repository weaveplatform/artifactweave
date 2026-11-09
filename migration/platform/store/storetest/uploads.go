package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/session"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
)

// runUploads covers upload sessions.
func runUploads(t *testing.T, newStore func(*testing.T) Store) {
	t.Run("Uploads", func(t *testing.T) { testUploads(t, newStore(t).Uploads()) })
}

func testUploads(t *testing.T, s session.UploadStore) {
	ctx := context.Background()
	u1 := session.Upload{
		ID: "u1", Backend: "s3", RepoKey: "docker-local", Path: "img/blobs", State: []byte("abc"),
		Bytes: 3, Started: storekit.At(1), Updated: storekit.At(5),
	}
	u2 := session.Upload{ID: "u2", Backend: "fs", Started: storekit.At(2)}
	u3 := session.Upload{ID: "u3"}
	u4 := session.Upload{ID: "u4", Started: storekit.At(1), Updated: storekit.At(10)}
	for _, u := range []session.Upload{u4, u3, u2, u1} {
		storekit.Must(t, s.Put(ctx, u))
	}
	storekit.Same(t, "Get", u1, storekit.Get[session.Upload](t)(s.Get(ctx, "u1")))
	storekit.WantErr(t, s.Put(ctx, session.Upload{Bytes: 1}), kernel.ErrInvalid)

	u1.Bytes, u1.State = 6, []byte("abcdef")
	storekit.Must(t, s.Put(ctx, u1))
	got := storekit.Get[session.Upload](t)(s.Get(ctx, "u1"))
	storekit.Same(t, "Get after replace", u1, got)
	got.State[0] = 'X'
	storekit.Same(t, "copies", u1, storekit.Get[session.Upload](t)(s.Get(ctx, "u1")))

	ids := func(us []session.Upload, err error) []string {
		t.Helper()
		storekit.Must(t, err)
		out := make([]string, 0, len(us))
		for _, u := range us {
			out = append(out, u.ID)
		}
		return out
	}
	storekit.Same(t, "Stale", []string{"u3", "u2", "u1"}, ids(s.Stale(ctx, storekit.At(6), 0)))
	storekit.Same(t, "Stale limited", []string{"u3", "u2"}, ids(s.Stale(ctx, storekit.At(6), 2)))
	storekit.Same(t, "Stale none", []string{"u3"}, ids(s.Stale(ctx, storekit.At(-100000), 0)))

	storekit.Must(t, s.Delete(ctx, "u1"))
	_, err := s.Get(ctx, "u1")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storekit.WantErr(t, s.Delete(ctx, "u1"), kernel.ErrNotFound)
}
