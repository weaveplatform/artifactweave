package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

var errBeforeCommitBusiness = errors.New("business failure")

// RunBeforeCommit proves that deferred event appends and business writes share
// one commit, including nested work and failure after an earlier append.
func RunBeforeCommit(t *testing.T, newStore func(*testing.T) Store) {
	t.Helper()
	for _, mode := range []string{"commit", "body failure", "append failure", "reentrant", "panic"} {
		t.Run(mode, func(t *testing.T) { beforeCommit(t, newStore(t), mode) })
	}
}

func beforeCommit(t *testing.T, s Store, mode string) {
	ctx := t.Context()
	storekit.Must(t, s.Settings().Put(ctx, "business", "before"))
	storekit.Get[outbox.Position](t)(s.Events().RegisterConsumer(ctx, "instance", "proof"))
	panicMarker := &struct{ message string }{"callback panic"}
	var escaped context.Context
	var recovered any
	var result error
	var after bool
	var callbacks int
	func() {
		defer func() { recovered = recover() }()
		result = s.Tx(ctx, func(ctx context.Context) error {
			escaped = ctx
			if err := s.Settings().Put(ctx, "business", "outer"); err != nil {
				return fmt.Errorf("outer business write: %w", err)
			}
			s.AfterCommit(ctx, func() { after = true })
			if err := tx.BeforeCommit(ctx, func(ctx context.Context) error {
				callbacks++
				if _, err := s.Events().AppendEvent(ctx, businessEvent(1)); err != nil {
					return fmt.Errorf("first event append: %w", err)
				}
				if mode == "panic" {
					panic(panicMarker)
				}
				if mode == "reentrant" {
					// The dropped registration error must still abort the unit.
					if err := tx.BeforeCommit(ctx, func(context.Context) error { return nil }); !errors.Is(err, tx.ErrBeforeCommit) {
						t.Errorf("reentrant registration = %v", err)
					}
				}
				return nil
			}); err != nil {
				return fmt.Errorf("first event registration: %w", err)
			}
			if err := s.Tx(ctx, func(ctx context.Context) error {
				if callbacks != 0 {
					t.Error("finalizer ran before nested business work finished")
				}
				if err := s.Settings().Put(ctx, "business", "nested"); err != nil {
					return fmt.Errorf("nested business write: %w", err)
				}
				return tx.BeforeCommit(ctx, func(ctx context.Context) error {
					callbacks++
					e := businessEvent(2)
					if mode == "append failure" {
						e.Topic = ""
					}
					_, err := s.Events().AppendEvent(ctx, e)
					if err != nil {
						return fmt.Errorf("second event append: %w", err)
					}
					return nil
				})
			}); err != nil {
				return fmt.Errorf("nested business transaction: %w", err)
			}
			if mode == "body failure" {
				return errBeforeCommitBusiness
			}
			return nil
		})
	}()
	wantErr := map[string]error{
		"body failure": errBeforeCommitBusiness, "append failure": kernel.ErrInvalid, "reentrant": tx.ErrBeforeCommit,
	}[mode]
	if mode == "panic" {
		if recovered != panicMarker {
			t.Fatalf("panic = %v; want original marker", recovered)
		}
	} else if recovered != nil || !errors.Is(result, wantErr) {
		t.Fatalf("transaction result = %v, panic = %v; want %v", result, recovered, wantErr)
	}
	// Reads after a recovered panic must acquire a released gate/connection.
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var value string
	storekit.Must(t, s.Settings().Get(check, "business", &value))
	batch, _, err := s.Events().Next(check, "instance", "proof", 10)
	storekit.Must(t, err)
	wantValue, wantEvents, wantCallbacks := "before", 0, 1
	switch mode {
	case "commit":
		wantValue, wantEvents, wantCallbacks = "nested", 2, 2
	case "body failure":
		wantCallbacks = 0
	case "append failure":
		wantCallbacks = 2
	}
	if value != wantValue || len(batch) != wantEvents || callbacks != wantCallbacks || after != (mode == "commit") {
		t.Fatalf("value=%q, events=%d, callbacks=%d, AfterCommit=%t; want %q, %d, %d, %t",
			value, len(batch), callbacks, after, wantValue, wantEvents, wantCallbacks, mode == "commit")
	}
	if mode == "commit" && (string(batch[0].Payload) != `{"n":1}` || string(batch[1].Payload) != `{"n":2}`) {
		t.Fatalf("nested event facts reordered: %+v", batch)
	}
	next := storekit.Get[outbox.Event](t)(s.Events().AppendEvent(check, businessEvent(3)))
	if next.Seq != int64(wantEvents+1) {
		t.Fatalf("sequence after transaction = %d; want %d", next.Seq, wantEvents+1)
	}
	if err := tx.BeforeCommit(escaped, func(context.Context) error { return nil }); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatalf("registration on escaped transaction context = %v", err)
	}
}
