package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// testHash is the Argon2id cost these tests hash with. Verification reads
// the cost from each stored hash, so a cheap hash checks like a real one;
// the production 64 MiB hash, once per seed and once per authenticator,
// made this package take minutes under the race detector.
var testHash = support.TestHash

// cheapHash is a cheap hash of "pw", the password of every test principal.
var cheapHash = support.CheapHash

// cheapSeed seeds db with the built-in principals, admin's password "pw".
func cheapSeed(t *testing.T, db store.Store) {
	t.Helper()
	support.Seed(t, db)
}
