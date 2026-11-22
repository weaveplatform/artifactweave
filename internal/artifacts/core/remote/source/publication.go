package source

import "context"

// Publication narrows one selected file read to current execution authority.
// Check refreshes target enablement/configuration/global controls without
// network I/O. Fence checks the worker lease with a fresh clock as the final
// operation in a cache publication transaction, after all domain locks/writes.
// A failure rolls back that transaction. Neither callback grants source access
// or replaces the ordinary caller, source and transport checks.
type Publication struct {
	Check func(context.Context) error
	Fence func(context.Context) error
}

// CheckPublication applies the optional execution restriction. Source calls it
// before backend work and delivery; the cache backend repeats it before writes.
func (q Request) CheckPublication(ctx context.Context) error {
	if q.Publication == nil {
		return nil
	}
	if q.Publication.Check == nil {
		return ErrInvalidRequest
	}
	return q.Publication.Check(ctx) //nolint:wrapcheck // caller maps the authority error without disclosure
}

// FencePublication ends a selected cache mutation, after every domain effect
// and before commit. Nothing may acquire a domain lock after this callback.
func (q Request) FencePublication(ctx context.Context) error {
	if q.Publication == nil {
		return nil
	}
	if q.Publication.Fence == nil {
		return ErrInvalidRequest
	}
	return q.Publication.Fence(ctx) //nolint:wrapcheck // preserve the owner's lease refusal
}
