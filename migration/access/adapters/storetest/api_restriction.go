package storetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func runAPIRestrictions(t *testing.T, factory Factory) {
	t.Run("APIRestrictions", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		for _, name := range []string{"alice", "bob"} {
			storekit.Must(t, s.Users().Create(ctx, user(1, name)))
		}
		a := s.APIRestrictions()
		_, err := a.Get(ctx, "alice")
		storekit.WantErr(t, err, store.ErrNotFound)
		r := store.APIRestriction{
			User:    "alice",
			Actor:   "admin",
			Reason:  "Expensive polling",
			Changed: storekit.Base,
			Until:   storekit.At(10),
		}
		first := storekit.Get[store.APIRestriction](t)(a.Put(ctx, r))
		if first.Sequence < 1 {
			t.Fatal("missing audit sequence")
		}
		storekit.Same(
			t,
			"current",
			first,
			storekit.Get[store.APIRestriction](t)(a.Get(ctx, "alice")),
		)
		storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
			_, err := a.Put(ctx, r)
			if err != nil {
				return err
			}
			return nil
		}))
		err = s.Tx(ctx, func(ctx context.Context) error {
			r.Until = time.Time{}
			if _, err := a.Put(ctx, r); err != nil {
				return err
			}
			return store.ErrConflict
		})
		storekit.WantErr(t, err, store.ErrConflict)
		current := storekit.Get[store.APIRestriction](t)(a.Get(ctx, "alice"))
		if current.Until.IsZero() {
			t.Fatal("rolled-back clear persisted")
		}
		history, next, err := a.History(ctx, "alice", store.Page{Limit: 1})
		storekit.Must(t, err)
		if len(history) != 1 || next == "" || history[0].Sequence != current.Sequence {
			t.Fatalf("first page: %+v %q", history, next)
		}
		history, end, err := a.History(ctx, "alice", store.Page{Limit: 1, After: next})
		storekit.Must(t, err)
		if len(history) != 1 || end != "" || history[0].Sequence != first.Sequence {
			t.Fatalf("last page: %+v %q", history, end)
		}
		r.User, r.Reason, r.Changed = "bob", "Recovery", storekit.At(1)
		storekit.Get[store.APIRestriction](t)(a.Put(ctx, r))
		history, _, err = a.History(ctx, "", store.Page{})
		storekit.Must(t, err)
		if len(history) != 3 || history[0].User != "bob" || !history[0].Until.IsZero() {
			t.Fatalf("audit: %+v", history)
		}
		storekit.Must(t, s.Users().Delete(ctx, "alice"))
		storekit.Must(t, s.Users().Create(ctx, user(1, "alice")))
		_, err = a.Get(ctx, "alice")
		storekit.WantErr(t, err, store.ErrNotFound)
		history, _, err = a.History(ctx, "alice", store.Page{})
		storekit.Must(t, err)
		if len(history) != 2 {
			t.Fatal("user deletion lost the audit")
		}
		r.User = "absent"
		_, err = a.Put(ctx, r)
		storekit.WantErr(t, err, store.ErrNotFound)
		for _, cursor := range []string{"invalid!", store.Cursor("0"), store.Cursor("no"), store.Cursor("1", "2")} {
			_, _, err := a.History(ctx, "", store.Page{After: cursor})
			storekit.WantErr(t, err, store.ErrInvalidCursor)
		}
		_, _, err = a.History(ctx, "", store.Page{Limit: -1})
		storekit.Must(t, err)
		for _, invalid := range []store.APIRestriction{{}, {User: "alice", Actor: "admin", Reason: "x", Changed: storekit.Base, Until: storekit.Base}} {
			_, err := a.Put(ctx, invalid)
			storekit.WantErr(t, err, store.ErrInvalid)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		r.User = "alice"
		_, err = a.Put(canceled, r)
		storekit.WantErr(t, err, context.Canceled)
		_, err = a.Get(canceled, "alice")
		storekit.WantErr(t, err, context.Canceled)
		_, _, err = a.History(canceled, "", store.Page{})
		storekit.WantErr(t, err, context.Canceled)
	})
	t.Run("APIRestrictionConcurrentAudit", func(t *testing.T) {
		s := factory(t)
		ctx := t.Context()
		storekit.Must(t, s.Users().Create(ctx, user(1, "alice")))
		r := store.APIRestriction{
			User:    "alice",
			Actor:   "admin",
			Reason:  "Concurrent update",
			Changed: storekit.Base,
			Until:   storekit.At(10),
		}
		var wg sync.WaitGroup
		for range 12 {
			wg.Go(func() {
				if _, err := s.APIRestrictions().Put(ctx, r); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		history, next, err := s.APIRestrictions().History(ctx, "alice", store.Page{})
		storekit.Must(t, err)
		if len(history) != 12 || next != "" {
			t.Fatalf("lost concurrent audit rows: %+v", history)
		}
		current := storekit.Get[store.APIRestriction](t)(s.APIRestrictions().Get(ctx, "alice"))
		storekit.Same(t, "current is latest committed change", history[0], current)
	})
}
