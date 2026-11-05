package api

import (
	"context"
	"errors"
)

// ErrScanLimit reports a permission scan that found more permissions than
// the caller's limit. The caller refuses its operation rather than act on a
// partial answer.
var ErrScanLimit = errors.New("access: permission scan limit exceeded")

// PathPair is a path and the path its content takes after a move, such as a
// repository migration. Folder marks a pair of folders, which a permission
// target also admits when it admits a path below them.
type PathPair struct {
	Source, Destination string
	Folder              bool
}

// Grants scans the stored permissions for questions about content rather
// than about one subject: who holds what on an item, and whether moving
// content would change who may reach it. Only the artifact sections of the
// permissions count. Each call uses ctx for the store it reads, so a call
// inside a store transaction takes part in it.
type Grants interface {
	// ItemGrants returns the actions each user and each group holds on the
	// item at path, a folder when folder is true, through the permissions
	// whose artifact section names one of targets with include and exclude
	// patterns that admit path. targets are tried in order, and a permission
	// counts at most once, for the first of them whose patterns admit path;
	// a user's or group's actions are the union over the permissions that
	// count. A store error names the target it was reading.
	ItemGrants(
		ctx context.Context,
		targets []string,
		path string,
		folder bool,
	) (users, groups map[string]Actions, err error)
	// AccessChange returns the name of the first permission, in name order,
	// whose artifact targets covering the repository repoKey of class admit
	// some pair's source and destination differently, or "" when none does.
	// It reads the permissions in pages of limit, which must be positive,
	// and returns ErrScanLimit once it reads more than limit permissions; a
	// store error is returned as the store gave it.
	AccessChange(
		ctx context.Context,
		repoKey, class string,
		pairs []PathPair,
		limit int,
	) (permission string, err error)
}
