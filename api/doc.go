// Package api holds the authored OpenAPI documents (decision record 0002) and
// the parity test that checks them against the gitignored cache of JFrog's
// published definitions. The bundled documents are embedded so the server can
// register a 501 route for every hand-written operation that has no handler
// yet: the generated strict servers get that from stubgen, hand-written routes
// need the spec itself.
package api

import (
	"embed"
	"io/fs"
	"sort"
)

// Specs are the bundled documents, one per service, as `make bundle` writes
// them. Fragments under src/ are not embedded; they are inputs.
//
//go:embed *.openapi.yaml
var Specs embed.FS

// SpecFiles lists the embedded documents in a stable order. It is derived
// from the embedded files so a new service needs no change here.
var SpecFiles = func() []string {
	files, err := fs.Glob(Specs, "*.openapi.yaml")
	if err != nil {
		// The pattern is a constant; Glob only fails on a malformed pattern.
		panic(err)
	}
	sort.Strings(files)
	return files
}()
