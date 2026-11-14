// Package httpkit holds the HTTP helpers every module's handlers share: the
// request context values the middleware leaves for handlers, the selection
// of an error response profile by ordered path rules, error and JSON
// writing, the caller checks handlers start with, and the conversion of
// OpenAPI path templates to chi patterns.
//
// # Role
//
// platform layer, httpkit: the module-agnostic part of the HTTP surface.
// What an ecosystem or module decides, such as which paths answer in npm's
// or Docker's error envelope, reaches it as a value (a ProfileRule list)
// rather than an import, so that any module's HTTP adapter can call these
// helpers without importing the server or another module. Its subpackages
// are apierror (the envelopes), baseurl (the public base URL) and
// middleware (request IDs, logging, recovery and request shaping).
//
// # Inputs
//
//   - Driving: the server's router, which installs ErrorProfiles with its
//     rule list first, and the handlers and middleware that read and write
//     the request context (WithRequestID and RequestIDFrom, WithAccept and
//     Accepts, NoteCause, NoteUser and the rest of context.go).
//   - The error profile rules: the server's ordered list, carried on each
//     request's context (ErrorProfiles, or WithProfileRules for a test that
//     calls a handler without the router).
//   - Driven: none. It writes only to the response it is given and keeps no
//     state of its own.
//
// # Outputs
//
//   - Context: WithRequestID, RequestIDFrom, WithOriginalContentType,
//     OriginalContentType, WithAccept, Accepts, WithRawBody, RawBody,
//     WithCause, NoteCause, WithUser, NoteUser, WithSearchQuery and
//     RawSearchQuery.
//   - Error profiles: ProfileRule, ProfileFor, ErrorProfiles, ErrorProfile
//     and WithProfileRules.
//   - Writing: WriteErr, WriteJSON, EscapePath and FormatTime.
//   - Streamed lists: StreamList reads a list's first store page and
//     returns a ListStream, whose Write sends the list page by page;
//     ListOptions name its content type and Envelope, which ArrayEnvelope
//     and EnvelopeOf build.
//   - Callers: AdminOnly, Principal, Denied and BadRequestf, over the
//     kernel's principal helpers.
//   - Routes: ChiPatterns and NotAPI. Pointers: Deref, DerefSlice and
//     PtrTo, for the optional fields of generated types.
//
// # Invariants
//
//   - The first rule that matches a path wins, so a narrower rule comes
//     before a broader one with the same prefix; with no match, or no rules
//     on the context, the profile is apierror.ProfileArtifactory, the same
//     for unclaimed paths as for handled ones.
//   - WriteErr sends the cause of an error that is not an *apierror.Error to
//     the access log through NoteCause, because the client sees only a bare
//     500.
//   - NoteCause and NoteUser do nothing on a context that never passed the
//     access log, so a handler called on its own neither panics nor logs.
//   - The caller checks answer with the kernel's typed errors
//     (kernel.ErrUnauthenticated, kernel.ErrForbidden), which carry their
//     status; AdminOnly wraps them as "repositories: %w", text clients
//     have always seen.
//   - A streamed list's bytes are those WriteJSON would send for the
//     collected list: the same element encoding, envelope and trailing
//     newline. It reads its first page before writing, so a store failure
//     there answers as a collected list's would. A failure after the first
//     byte aborts the response with http.ErrAbortHandler and notes the
//     cause, and the closing bracket is never written, so no client takes
//     a cut list for a whole one (decision 0043, "Streaming complete
//     lists").
//   - It imports no module, as the platform layer must not.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md
//     (the error response profiles, which only their selection here
//     changes).
//   - Decision 0016: docs/research/decisions/0016-observability-and-health.md
//     (request IDs, and the access log line that names the user, never the
//     credential).
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md
//     ("Streaming complete lists").
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": the platform imports no module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 158 to 163 (platform/httpkit and its middleware) and lines 40 and
//     41 (dependency rule 4).
//   - https://github.com/weaveplatform/artifactweave/issues/1069,
//     https://github.com/weaveplatform/artifactweave/issues/1071 and
//     https://github.com/weaveplatform/artifactweave/issues/1409 (streamed
//     lists)
//
// # Status
//
// Implemented P0 to P9c, as the server root's helpers: the context values
// and JSON and error writing from the baseline, the access log's cause and
// user slots and the property search's raw query in later phases. P9d
// (WEB-03) moved them here and turned the server's error-profile switch into
// ordered rules carried on the request context; WEB-07 added the caller
// checks. PAGE-02 (#1409) added the streamed lists.
//
// # Testing
//
// The router's guards in app and the route suites under test/server reach
// these helpers through the router; TestAccP8ErrorProfiles and
// TestAccStubsAndEnvelopes check the profiles the rules choose. The unit tests
// here cover the branches those suites do not reach: rule order and the
// default, a request without rules, the context values on a bare context, the
// pointers' nil cases, the route patterns and each caller check's answers.
// stream_test.go holds a streamed list to the bytes WriteJSON sends for the
// same list, empty, short and over several pages, filtered and enveloped,
// and covers a failure before the first byte, after it, an expired
// deadline between pages and a failed write.
package httpkit
