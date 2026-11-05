package storetest

import (
	"context"
	"testing"

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
		Bytes: 3, Started: at(1), Updated: at(5),
	}
	u2 := store.Upload{ID: "u2", Backend: "fs", Started: at(2)}
	u3 := store.Upload{ID: "u3"}
	u4 := store.Upload{ID: "u4", Started: at(1), Updated: at(10)}
	for _, u := range []store.Upload{u4, u3, u2, u1} {
		must(t, s.Put(ctx, u))
	}
	same(t, "Get", u1, get[store.Upload](t)(s.Get(ctx, "u1")))
	wantErr(t, s.Put(ctx, store.Upload{Bytes: 1}), store.ErrInvalid)

	u1.Bytes, u1.State = 6, []byte("abcdef")
	must(t, s.Put(ctx, u1))
	got := get[store.Upload](t)(s.Get(ctx, "u1"))
	same(t, "Get after replace", u1, got)
	got.State[0] = 'X'
	same(t, "copies", u1, get[store.Upload](t)(s.Get(ctx, "u1")))

	ids := func(us []store.Upload, err error) []string {
		t.Helper()
		must(t, err)
		out := make([]string, 0, len(us))
		for _, u := range us {
			out = append(out, u.ID)
		}
		return out
	}
	same(t, "Stale", []string{"u3", "u2", "u1"}, ids(s.Stale(ctx, at(6), 0)))
	same(t, "Stale limited", []string{"u3", "u2"}, ids(s.Stale(ctx, at(6), 2)))
	same(t, "Stale none", []string{"u3"}, ids(s.Stale(ctx, at(-100000), 0)))

	must(t, s.Delete(ctx, "u1"))
	_, err := s.Get(ctx, "u1")
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, s.Delete(ctx, "u1"), store.ErrNotFound)
}
