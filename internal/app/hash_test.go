package app

import "github.com/weaveplatform/artifactweave/internal/auth"

// Tests hash with a tiny Argon2id cost: every runtime they open seeds the
// admin and builds an authenticator, and the production 64 MiB hash made
// this package take minutes under the race detector. Verification reads the
// cost from each stored hash, so the seeded passwords still check.
func init() {
	authHash = auth.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}
	// The smallest RSA key Go accepts: each runtime generates a signing key.
	tokenKeyBits = 1024
}
