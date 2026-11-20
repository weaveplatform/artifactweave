package api

import "github.com/weaveplatform/artifactweave/internal/platform/outbox"

// UserLocked is the durable account lock transition, independent of a vendor envelope.
const UserLocked = "user.locked"

// UserLockedEvent contains only public identity and a sanitized actor.
// Passwords, hashes, API keys and token identifiers are never event facts.
type UserLockedEvent struct {
	Version int          `json:"version"`
	Outcome string       `json:"outcome"`
	Actor   outbox.Actor `json:"actor"`
	User    string       `json:"user"`
}
