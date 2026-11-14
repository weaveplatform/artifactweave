// Package apierror renders errors with the response profile each client expects.
// Artifactory, Access, the OCI distribution API, Xray, npm and the Terraform
// registry all use different JSON shapes, and clients read them differently:
// jfrog-client-go prints the whole body after the status (only its release
// bundle import reads errors[0].message, and its Xray scan reads error), the
// Terraform providers join every errors[] entry, and Docker clients read
// errors[].code. One table here is the only place a shape is defined. See
// decision record 0002.
//
// # Role
//
// platform layer, in platform/httpkit: the error responses of the whole HTTP
// surface, which no module owns. Handlers write their errors with Write, the
// server's error handlers for the generated strict servers render through
// it, and the 501 stubs that tools/stubgen writes return ErrNotImplemented.
// The kernel's authentication sentinels are Error values; decision 0044 lets
// the kernel import this one platform package. It imports only the standard
// library: encoding/json, errors, net/http, strconv and strings.
//
// # Inputs
//
//   - A handler's error: an *Error from New or NotSupported, or one built
//     with an OCI or Access code, a Detail, NoChallenge or RetryAfterSeconds;
//     an error that wraps an *Error, which Write finds with errors.As; or any
//     other error.
//   - The Profile of the route, which the caller chooses by request path: the
//     server's ordered rules (errorProfileRules in app's router, read through
//     httpkit.ErrorProfile and httpkit.ProfileFor) map path prefixes to
//     profiles, unclaimed paths included, and each format's routes name their
//     own.
//   - The response headers a handler set before the error: Write keeps a
//     WWW-Authenticate challenge that is already there.
//
// # Outputs
//
//   - Write: the status, the profile's envelope as JSON (the message as
//     plain text under ProfileText) and the profile's content type:
//     application/json, application/vnd.api+json for the Terraform backend,
//     application/problem+json for APM and the Swift registry, and
//     application/vnd.pub.v2+json for Dart pub. A Swift registry error also
//     carries Content-Version: 1, without which SwiftPM does not show its
//     detail (#1278). A 401 gets a challenge, BasicChallenge or, under
//     ProfilePub, Bearer realm="pub" with the error's message, as the pub
//     repository specification requires (#1279), unless the error sets
//     NoChallenge or the response has one already. So does an error that
//     sets Challenge, such as the 403 7.x's entry point answers a refused
//     API key with, and under ProfilePub any 403. A positive
//     RetryAfterSeconds sets Retry-After.
//   - Profile and its sixteen values; Error, with New, NotSupported and
//     ErrNotImplemented; and BasicChallenge, Artifactory's realm.
//
// # Invariants
//
//   - An error that is not an *Error becomes a 500 with the message
//     "internal error", so no internal detail reaches a client.
//   - Envelopes, statuses, content types, challenges and messages are what
//     JFrog's clients and the native package clients read or print, so they
//     stay byte-identical.
//   - New sets Code from the status, the code that the Access, Grid and Pub
//     envelopes carry when a handler gives none: the name of the status's
//     constant in JAX-RS's Response.Status, as 7.x Access names its codes,
//     which is Go's status text in upper case with underscores for spaces
//     (BAD_REQUEST, METHOD_NOT_ALLOWED, REQUEST_ENTITY_TOO_LARGE,
//     TOO_MANY_REQUESTS, SERVICE_UNAVAILABLE). 500 is INTERNAL_ERROR, as is
//     a status Go has no text for, and 501 is UNSUPPORTED. A status JAX-RS
//     has no constant for keeps only the letters of Go's text (418 is
//     IM_A_TEAPOT), so every code is upper-case letters and underscores.
//   - Under ProfileOCI, an error whose code is empty or its status's name
//     carries the distribution specification's code for a 401, 403 or 429
//     instead: UNAUTHORIZED, DENIED or TOOMANYREQUESTS. Write computes it
//     and never changes the error, since the kernel's errors are shared by
//     every surface.
//   - It imports nothing beyond the standard library. Decision 0044 holds the
//     kernel to the standard library and this package, so any other import
//     would reach the kernel too, and an import of the kernel would be a
//     cycle.
//   - The package name is part of the lint configuration: wrapcheck's
//     extra-ignore-sigs in .golangci.yml lists apierror.New( by text, so
//     under another name every return of New would become a finding.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md
//     ("Error response profiles", with the table of profiles).
//   - Decision 0001: docs/research/decisions/0001-mirror-the-jfrog-api-surface.md
//     (the envelopes are JFrog's, so that JFrog's clients read them).
//   - Research 10: docs/research/10-scope-and-tiers.md, "Error envelopes by
//     surface".
//   - The codes: JAX-RS's Response.Status
//     (https://github.com/jakartaee/rest/blob/2.1.6/jaxrs-api/src/main/java/javax/ws/rs/core/Response.java);
//     7.x Access bodies that pair UNAUTHORIZED with JAX-RS's message "HTTP
//     401 Unauthorized" (https://jfrog.atlassian.net/browse/RTFACT-23421)
//     and answer a 500 with INTERNAL_SERVER_ERROR
//     (https://jfrog.atlassian.net/browse/RTFACT-30977); the Error schemas
//     of the Projects and Grid specs; and the hosted pub repository spec v2,
//     "Rejecting Requests"
//     (https://github.com/dart-lang/pub/blob/master/doc/repository-spec-v2.md),
//     whose code is free text that the Dart client does not read.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": the kernel imports only the standard library and
//     platform/httpkit/apierror; "Dependency rules"; "Constraints": the lint
//     exceptions match package names).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 158 to 160 (platform/httpkit), line 1018 (the mapping row) and
//     lines 40 and 41 (the platform imports no module).
//   - Plan: docs/implementation/plan.md#p0-foundations, task 5;
//     #p1-data-model-and-api-schemas, task 3;
//     #p1b-enterprise-products-specs-and-model, task 1; and
//     #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/1000 and
//     https://github.com/weaveplatform/artifactweave/issues/1236 (a code
//     for every status).
//   - The OCI codes: the distribution specification's error codes
//     (https://github.com/opencontainers/distribution-spec/blob/v1.1.0/spec.md#error-codes),
//     decision 0031 (docs/research/decisions/0031-docker-oci-registry.md),
//     and https://github.com/weaveplatform/artifactweave/issues/1248.
//
// # Status
//
// Implemented P0 to P1b, which arrived together in the baseline commit: the
// error table by surface (P0, plan task 5), ErrNotImplemented for the 501
// stubs (P1, task 3) and the Distribution, platform federation and topology,
// and legacy Artifactory envelopes (P1b, task 1). P3 added the Basic
// challenge on a 401 (#43), and the 7.x envelope audit gave platform
// federation, topology, and import and export envelopes of their own (#404).
// Later phases added the profiles and fields of their surfaces: the
// Terraform backend (P8, #558), NoChallenge and RetryAfterSeconds for remote
// sources (P9, #616), APM (P9b, #692), and the Swift registry and Dart pub
// (P9c, #766 and #765). Moved here in P9d. New takes the code from the
// status's name (#1236). The OCI envelope gives a 401, 403 or 429 the
// specification's code, and a code keeps only letters and underscores
// (#1248). A Swift registry error carries Content-Version: 1 (#1278).
//
// # Testing
//
//   - TestUnitWriteEnvelopes: the status, the application/json content type
//     and the envelope of eleven profiles, with and without Detail, for a
//     wrapped *Error, and the 500 that another error becomes.
//   - TestUnitWriteText: the plain-text body and content type of ProfileText.
//   - TestUnitTerraformBackendEnvelope: the JSON:API document and its
//     content type.
//   - TestUnitUnauthorizedChallenges: the Basic challenge on a 401, a
//     handler's own challenge kept, none on a 403 unless the error sets
//     Challenge, and none on an upstream's 401 or 403 (NoChallenge).
//   - TestUnitPubChallenge: pub's challenge on a 401 and a 403, with the
//     error's message escaped into printable ASCII, and none on another
//     status or an upstream's refusal (#1279).
//   - TestUnitDefaultCodes: a table over status and profile. For each status
//     that a handler, the middleware or a remote's upstream can send through
//     the Access, Grid and Pub envelopes, the code New derives, with the
//     status, content type and message, in each of the three; and the text
//     of Error.
//   - TestUnitOCICodes: the OCI envelope's code for an error built with New,
//     one with no code and one a handler gave a code, and that rendering
//     leaves the error unchanged.
//   - TestUnitProfileHeaders: the exact headers of every profile for a 404,
//     and for a 401 with Retry-After: the content type, the challenge, Basic
//     or pub's Bearer with the message, Retry-After, and Content-Version: 1 under the Swift
//     registry profile and no other.
//
// These tests leave the APM and Swift registry problem JSON bodies to the
// tests of the routes that write them: the Agent Packages suite and the
// Swift format tests. Over HTTP, the server's
// TestAccStubsAndEnvelopes checks the 501 and 404 envelopes of the stubbed
// products and of unknown paths on each surface, and
// TestAccUnimplementedOperationsAnswer501 checks that every specified
// operation no handler implements answers 501.
package apierror
