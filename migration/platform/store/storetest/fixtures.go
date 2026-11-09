package storetest

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
)

// errBoom is the error a case returns to make a transaction roll back.
var errBoom = errors.New("boom")

// The helpers below are the federation suite's too, declared again
// because the platform suite cannot reach that suite's unexported helpers,
// and the same instance ids and keys keep the two suites' fixtures alike.

// cancelled returns a context that is already cancelled: every store call
// made with it must fail rather than read or write.
func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// failsCancelled asserts that each call fails on a cancelled context.
func failsCancelled(t *testing.T, calls map[string]func(context.Context) error) {
	t.Helper()
	ctx := cancelled()
	for name, call := range calls {
		if call(ctx) == nil {
			t.Errorf("%s succeeded on a cancelled context", name)
		}
	}
}

// peerID is the instance id of fixture instance i.
func peerID(i int) string {
	return fmt.Sprintf("jfrt@%08x-0000-4000-8000-%012x", i, i)
}

// peerKey is fixture instance i's Ed25519 key pair.
func peerKey(i int) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = byte(i) //nolint:gosec // fixture numbers are small, so the byte is the number
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv //nolint:forcetypeassert // ed25519 public keys are this type
}
