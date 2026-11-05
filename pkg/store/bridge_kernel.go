// Bridge (P9d): declared in migration/kernel; PRO-02 deletes it.

package store

import "github.com/weaveplatform/artifactweave/migration/kernel"

var (
	ErrNotFound      = kernel.ErrNotFound
	ErrConflict      = kernel.ErrConflict
	ErrInvalidCursor = kernel.ErrInvalidCursor
	ErrInvalid       = kernel.ErrInvalid
)

const (
	DefaultLimit = kernel.DefaultLimit
	MaxLimit     = kernel.MaxLimit
)

type Page = kernel.Page

var Cursor = kernel.Cursor

func Trim[T any](rows []T, limit int, key func(T) []string) ([]T, string) {
	return kernel.Trim(rows, limit, key)
}
func Clone[V any](v V) V     { return kernel.Clone(v) }
func Normalise[V any](v V) V { return kernel.Normalise(v) }
