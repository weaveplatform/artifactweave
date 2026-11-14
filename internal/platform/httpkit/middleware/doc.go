// Package middleware holds the HTTP middleware that no module owns: request
// IDs, the access log, panic recovery, the management body limit, vendor
// media type normalisation, trailing-slash collection paths, and the error
// handler of the generated strict servers.
//
// # Role
//
// platform layer, httpkit/middleware: the request handling around every
// route. Each decision that belongs to an ecosystem or to the server's
// configuration arrives as a hook: AccessLog and RecoverPanic log a path
// through a redactor, NormalizeContentType leaves alone the requests a keep
// predicate accepts, and CollectionSlash rewrites the paths of a map. Error
// responses take their profile from the rules on the request context
// (httpkit.ErrorProfile), so no middleware here names a path prefix.
//
// # Inputs
//
//   - Driving: the server's router, which installs RequestID, RecoverPanic,
//     AccessLog, NormalizeContentType and CollectionSlash on every request,
//     BodyLimit on its JSON management groups, and BindingError as the
//     request and response error handler of each generated strict server.
//   - Hooks: the redactor of AccessLog and RecoverPanic, the keep predicate
//     of NormalizeContentType and the path map of CollectionSlash.
//   - The request context: the error profile rules (httpkit.ErrorProfiles)
//     and the slots AccessLog gives its handlers (httpkit.NoteCause and
//     httpkit.NoteUser).
//   - Driven: a *slog.Logger, the only thing it writes besides the response.
//
// # Outputs
//
//   - RequestID and RequestIDHeader, AccessLog, RecoverPanic, BodyLimit and
//     BodyTooLarge, NormalizeContentType, BindingError and CollectionSlash.
//
// # Invariants
//
//   - AccessLog writes one line per request with its method, redacted path,
//     status, bytes, duration and request ID; a request that authenticated
//     names its user, never the credential, and one whose handler noted a
//     cause logs at ERROR with it. A response its handler aborted with
//     http.ErrAbortHandler, as a streamed list does when its store fails
//     after the first byte, is logged too, at ERROR with "aborted":true,
//     before the panic goes on to net/http.
//   - A panic answers 500 "internal error" in the request's profile and is
//     logged; http.ErrAbortHandler is re-raised, as net/http expects.
//   - A body over BodyLimit's limit answers 413 with BodyTooLarge's text,
//     whether its length was declared or not.
//   - It imports no module, as the platform layer must not.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md
//     (the error response profiles of the strict servers' errors).
//   - Decision 0016: docs/research/decisions/0016-observability-and-health.md
//     (request IDs, and the access log line that names the user, never the
//     credential).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": the platform imports no module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 158 to 163 (platform/httpkit/middleware) and lines 40 and 41
//     (dependency rule 4).
//   - https://github.com/weaveplatform/artifactweave/issues/1069
//
// # Status
//
// Implemented P0 to P9c, as the server root's middleware.go: request IDs,
// the access log, recovery, the body limit, content-type normalisation and
// the binding errors from the baseline, the access log's cause and user in
// later phases, and the Terraform provider's trailing-slash collections.
// P9d (WEB-03) moved it here and turned its ecosystem decisions into hooks.
//
// # Testing
//
// middleware_test.go checks request ID reuse and minting, the access log's
// fields, level and cause, the line of an aborted response, panic recovery
// in two envelopes, the body limit with declared and undeclared lengths,
// content-type normalisation and the binding errors, each with its hooks
// passed explicitly. The router's guards
// in app hold TestAccStrictBindingErrorThroughRouter, which builds the router,
// and TestAccRegistryKeepsContentType, which checks its Docker keep predicate.
package middleware
