package api

import "context"

// Export reads the current descriptor-owned configuration as XML. Implementations
// collect all sections under the shared metadata transaction and authority guard,
// recheck the caller's current administrator authority there, and return bytes
// only after commit. An error returns no document. Credentials, sensitive header
// values and environment references are never included.
// XML refuses a context already inside the export store's transaction before
// reading state, so callers never receive provisional bytes. Section readers
// join the new transaction that export owns.
// HTTP bindings supply both current authentication and effective authorization;
// one-sided bindings fail. A trusted store embedding may omit both only when its
// caller establishes global administrator authority before invoking XML.
//
// Subsequent descriptor sections extend the core's typed export document and join
// this same read, using their owning module's api with its transaction context.
// Reading a section must perform no network I/O. A new producer must use the same
// guard as the descriptor and repository writes before its section is exported.
type Export interface {
	XML(ctx context.Context) ([]byte, error)
}
