package tx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

func TestUnitPublicationCheckRegistrationAndFailure(t *testing.T) {
	if err := tx.CheckPublication(t.Context()); err != nil {
		t.Fatal("unconfigured composition changed behavior", err)
	}
	if err := tx.SetPublicationCheck(t.Context(), func(context.Context) error { return nil }); !errors.Is(err, tx.ErrBeforeCommit) {
		t.Fatal("publication check installed outside a transaction", err)
	}
	marker := errors.New("publication refused")
	var f tx.Finalizers
	defer f.Close()
	ctx := f.Bind(t.Context())
	if err := tx.SetPublicationCheck(ctx, func(context.Context) error { return marker }); err != nil {
		t.Fatal(err)
	}
	if err := tx.CheckPublication(ctx); !errors.Is(err, marker) {
		t.Fatal("publication refusal was lost", err)
	}
}

func TestUnitPublicationCheckRefusesReplacementAndLateRegistration(t *testing.T) {
	for _, mode := range []string{"nil", "replacement", "finalizing", "closed"} {
		t.Run(mode, func(t *testing.T) {
			var f tx.Finalizers
			defer f.Close()
			ctx := f.Bind(t.Context())
			check := func(context.Context) error { return nil }
			switch mode {
			case "nil":
				check = nil
			case "replacement":
				if err := tx.SetPublicationCheck(ctx, check); err != nil {
					t.Fatal(err)
				}
			case "closed":
				f.Close()
			case "finalizing":
				if err := tx.BeforeCommit(ctx, func(ctx context.Context) error {
					if err := tx.SetPublicationCheck(ctx, check); !errors.Is(err, tx.ErrBeforeCommit) {
						t.Fatal("finalizer installed new publication work", err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := f.Run(ctx); !errors.Is(err, tx.ErrBeforeCommit) {
					t.Fatal("dropped finalizer registration error did not fail commit", err)
				}
				return
			}
			if err := tx.SetPublicationCheck(ctx, check); !errors.Is(err, tx.ErrBeforeCommit) {
				t.Fatal("invalid publication check registration accepted", err)
			}
			if mode != "closed" && !errors.Is(f.Run(ctx), tx.ErrBeforeCommit) {
				t.Fatal("dropped registration error did not fail commit")
			}
		})
	}
}

func TestUnitPublicationSettlementCanStageCommitFacts(t *testing.T) {
	var f tx.Finalizers
	defer f.Close()
	ctx := f.Bind(t.Context())
	staged, finalized := false, false
	if err := tx.SetPublicationCheck(ctx, func(ctx context.Context) error {
		if staged {
			return nil
		}
		staged = true
		return tx.BeforeCommit(ctx, func(context.Context) error { finalized = true; return nil })
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := tx.CheckPublication(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !staged || finalized {
		t.Fatal("settlement ran commit facts early")
	}
	if err := f.Run(ctx); err != nil || !finalized {
		t.Fatal("staged publication facts did not finalize", err)
	}
}
