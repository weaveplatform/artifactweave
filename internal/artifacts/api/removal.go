package api

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// RemovalRequest names one selected indexed file, never a recursive subtree.
// PropertyNames includes every property used by selection, including names that
// were absent. Credential is a private reference to the original verified caller.
type RemovalRequest struct {
	Credential    kernel.CredentialReference
	Item          IndexFile
	PropertyNames []string
	SkipTrashcan  bool
}

// RemovedItem records an actual committed candidate or format-owned companion.
// It retains no properties, actor credentials or repository connection details.
type RemovedItem struct {
	ID     int64
	Path   string
	Size   int64
	SHA256 string
}

// RemovalResult includes the normal item service's actual companion removals.
// A caller must not infer these counts from an earlier candidate listing.
type RemovalResult struct {
	Items []RemovedItem
	Files int64
	Bytes int64
}

// ItemRemoval rechecks the original credential, native Delete permission and
// exact content/property/download snapshot under the shared publication guard.
// Remote caches, folders and configured-member federated repositories are refused.
//
// Revalidate checks current policy/selection/comparator facts after the item is
// locked. Record runs after normal item/trash/index/hook/event mutations to
// acknowledge their actual result and fence the current run/lease. Both callbacks
// join the same transaction and perform local metadata operations only. A failed
// callback rolls back every mutation; no transaction may span network I/O.
// A missing file returns ErrNotFound, changed facts ErrConflict and denied native
// permission ErrForbidden. No partial result is returned on any failure.
type ItemRemoval interface {
	Remove(context.Context, RemovalRequest, func(context.Context) error, func(context.Context, RemovalResult) error) (RemovalResult, error)
}
