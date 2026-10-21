package item

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestTransferWorkOrderingAndRollback(t *testing.T) {
	for _, mode := range []string{"commit", "hook failure", "enqueue failure"} {
		t.Run(mode, func(t *testing.T) {
			w, r := recording(t)
			w.must("outer", "outer content")
			w.must("inner", "inner content")
			q := &transferQueueRecorder{store: w.db}
			if mode == "enqueue failure" {
				q.fail = true
			}
			w.svc.Work = q
			r.deployed = func(ctx context.Context, env format.Env, it types.Item) error {
				if it.Name == "inner" {
					if err := env.Enqueue(ctx, "a", "z", []byte(`"inner"`)); err != nil {
						return err
					}
					return env.Enqueue(ctx, "z", "a", nil)
				}
				payload := []byte(`"first"`)
				if err := env.Enqueue(ctx, "z", "z", payload); err != nil {
					return err
				}
				payload[0] = 'X'
				res, err := w.svc.Transfer(
					ctx,
					Transfer{
						From: Target{Repo: w.loc, Path: "inner"},
						To:   Target{Repo: w.loc, Path: "copied/inner"},
						By:   "test",
					},
				)
				if err != nil {
					return err
				}
				if res.Files != 1 {
					t.Fatalf("nested transfer: %+v", res)
				}
				if len(q.keys) != 0 {
					t.Fatal("nested transfer flushed before its parent")
				}
				if err := env.Enqueue(ctx, "z", "z", []byte(`"last"`)); err != nil {
					return err
				}
				if mode == "hook failure" {
					return errBoom
				}
				return nil
			}
			_, err := w.svc.Transfer(
				t.Context(),
				Transfer{
					From: Target{Repo: w.loc, Path: "outer"},
					To:   Target{Repo: w.loc, Path: "copied/outer"},
					By:   "test",
				},
			)
			if mode == "commit" {
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(q.keys, []string{"a/z", "z/a", "z/z"}) {
					t.Fatal(q.keys)
				}
				it, err := w.db.WorkQueue().Get(t.Context(), "z", "z")
				if err != nil || string(it.Payload) != `"last"` {
					t.Fatalf("last payload %s %v", it.Payload, err)
				}
				if _, ok := w.get(w.loc, "copied/inner"); !ok {
					t.Fatal("nested copy missing")
				}
				return
			}
			if !errors.Is(err, errBoom) {
				t.Fatal(err)
			}
			for _, p := range []string{"copied/inner", "copied/outer"} {
				if _, ok := w.get(w.loc, p); ok {
					t.Fatal("failed transfer committed", p)
				}
			}
			if _, err := w.db.WorkQueue().
				Get(t.Context(), "a", "z"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("work survived rollback", err)
			}
			if mode == "hook failure" && len(q.keys) != 0 {
				t.Fatal("failed hook flushed work")
			}
		})
	}
}

type transferQueueRecorder struct {
	store store.Store
	keys  []string
	fail  bool
}

func (q *transferQueueRecorder) Enqueue(
	ctx context.Context,
	kind, key string,
	payload []byte,
) error {
	q.keys = append(q.keys, kind+"/"+key)
	if q.fail && len(q.keys) == 2 {
		return errBoom
	}
	return q.store.WorkQueue().
		Enqueue(ctx, kind, key, payload, at)
	//nolint:wrapcheck // test delegate
}
