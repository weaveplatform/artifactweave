package api

import "context"

// RepositoryLifetimes reads the current persistent repository incarnation
// without opening configuration secrets. It grants no authority. Callers hold
// their shared project guard and check current access before reading it.
type RepositoryLifetimes interface {
	RepositoryIncarnation(context.Context, string) (string, error)
}
