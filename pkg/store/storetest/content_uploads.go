package storetest

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// runUploads covers upload sessions.
func runUploads(t *testing.T, newStore Factory) {
	t.Run("Uploads", func(t *testing.T) { testUploads(t, newStore(t).Uploads()) })
}

func testUploads(t *testing.T, s store.UploadStore) {
	ctx := context.Background()
	u1 := store.Upload{
		ID: "u1", Backend: "s3", RepoKey: "docker-local", Path: "img/blobs", State: []byte("abc"),
		Bytes: 3, Started: storekit.At(1), Updated: storekit.At(5),
	}
	u2 := store.Upload{ID: "u2", Backend: "fs", Started: storekit.At(2)}
	u3 := store.Upload{ID: "u3"}
	u4 := store.Upload{ID: "u4", Started: storekit.At(1), Updated: storekit.At(10)}
	for _, u := range []store.Upload{u4, u3, u2, u1} {
		storekit.Must(t, s.Put(ctx, u))
	}
	storekit.Same(t, "Get", u1, storekit.Get[store.Upload](t)(s.Get(ctx, "u1")))
	storekit.WantErr(t, s.Put(ctx, store.Upload{Bytes: 1}), store.ErrInvalid)

	u1.Bytes, u1.State = 6, []byte("abcdef")
	storekit.Must(t, s.Put(ctx, u1))
	got := storekit.Get[store.Upload](t)(s.Get(ctx, "u1"))
	storekit.Same(t, "Get after replace", u1, got)
	got.State[0] = 'X'
	storekit.Same(t, "copies", u1, storekit.Get[store.Upload](t)(s.Get(ctx, "u1")))

	ids := func(us []store.Upload, err error) []string {
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
	storekit.WantErr(t, err, store.ErrNotFound)
	storekit.WantErr(t, s.Delete(ctx, "u1"), store.ErrNotFound)
}
