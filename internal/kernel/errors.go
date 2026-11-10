package kernel

import "errors"

// Sentinel errors. Implementations wrap them; callers test with errors.Is.
var (
	// ErrNotFound reports that a keyed record does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict reports a create that collides with an existing key, or a
	// change that would break a uniqueness rule.
	ErrConflict = errors.New("store: conflict")
	// ErrInvalidCursor reports a malformed pagination cursor.
	ErrInvalidCursor = errors.New("store: invalid cursor")
	// ErrInvalid reports a record the store cannot hold as given, for
	// example an item path that does not split into parent and name.
	ErrInvalid = errors.New("store: invalid record")
)
