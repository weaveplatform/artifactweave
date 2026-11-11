package configdoc

import "context"

// Watch is told about changes to config documents of one kind. Changed runs
// inside the write's transaction with its context: after a Put that creates
// the document or changes its Document bytes, and after every successful
// Delete. A Put that changes only Enabled, Updated or Secrets does not call
// it. An error from Changed fails the write and rolls it back. A store runs
// the watches of a kind in the order it was given them, and the first error
// stops the rest.
//
// It exists because a layout edit must fence the remote repositories that
// use the layout in the same transaction, so that a cache fill started
// under the old definition cannot publish (decision 0007), and the code
// that fences belongs to the artifacts module, which the platform may not
// import. The composition registers that fence as a watch of ConfigLayout.
type Watch struct {
	Kind    ConfigKind
	Changed func(ctx context.Context, key string) error
}
