package server

import (
	"context"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// testHash is the Argon2id cost these tests hash with. Verification reads
// the cost from each stored hash, so a cheap hash checks like a real one;
// the production 64 MiB hash, once per seed and once per authenticator,
// made this package take minutes under the race detector.
var testHash = auth.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}

// cheapHash is a cheap hash of "pw", the password of every test principal.
var cheapHash = func() []byte {
	h, err := auth.HashPasswordWith(testHash, "pw")
	if err != nil {
		panic(err)
	}
	return h
}()

// cheapSeed seeds db with the built-in principals, admin's password "pw".
func cheapSeed(t *testing.T, db store.Store) {
	t.Helper()
	if _, err := auth.SeedHashed(context.Background(), db, cheapHash, time.Now()); err != nil {
		t.Fatal(err)
	}
}
