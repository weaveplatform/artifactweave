// Package fault holds the failing fakes that handler tests share
// (decision 0015), so the error paths of streaming handlers are covered
// without real failures: a client that goes away mid-stream, and a blob
// store whose writes fail.
//
// # Role
//
// platform layer, test support (platform/testkit): failing fakes that
// cover the error paths of streaming handlers without real failures. Its
// users are handler and service tests that stream a request body into the
// blob store. Test files in any module may import it (dependency rule 1);
// production code never does. It imports platform/blob and the standard
// library.
//
// # Inputs
//
//   - For Disconnect: a request body and the number of bytes it gives
//     before the client goes away.
//   - For Blobs: the blob.Store to wrap and the errors to inject, one each
//     for BeginUpload (Begin), Append and Commit.
//
// # Outputs
//
//   - ErrDisconnected, the error a cut-off body's reads fail with once it
//     has given its bytes.
//   - Disconnect, which returns the cut-off body as an io.Reader.
//   - Blobs, a blob.Store whose BeginUpload fails, or returns an upload
//     whose Append or Commit fails. Its other methods, and the upload's,
//     are the wrapped store's.
//
// # Invariants
//
//   - ErrDisconnected wraps io.ErrUnexpectedEOF, the error net/http's
//     request body returns when a client closes the connection before the
//     length it declared, so a handler that tells a disconnect apart with
//     errors.Is sees one here too.
//   - A nil error in Blobs passes the call through to the wrapped store,
//     and the store's own errors come back unwrapped.
//   - An injected Append error comes after the wrapped upload has stored
//     what it read, with that count: a write that fails part way through.
//     An injected Commit error aborts the upload first, so the upload
//     leaves no session and commits nothing.
//   - ErrDisconnected's text, "testfault: client disconnected: unexpected
//     EOF", does not follow the package name: error text stays
//     byte-identical through P9d (decision 0044, "Behaviour").
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md,
//     "Fault injection": handler error paths are covered with shared
//     failing fakes rather than real failures.
//   - docs/implementation/definition-of-done.md, "Coverage rules":
//     streaming handlers reach the coverage floor through these fakes.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Package placement and names" (platform/testkit/fault, and the
//     package name fault) and "Dependency rules" (rule 1: test files may
//     import any test-support package across modules).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 169 and 170 (platform/testkit/fault: failing blob store,
//     vanishing client) and line 1046 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals
//     (groundwork 1, the router kit) and #p9d-modular-monolith-migration,
//     epic PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/532 and
//     https://github.com/weaveplatform/artifactweave/issues/984
//
// # Status
//
// Implemented P8: Disconnect, ErrDisconnected and Blobs came with the
// router kit's multipart uploads and the NuGet API key (issue 532, pull
// request 580). Staged in P9d.
//
// # Testing
//
//   - TestUnitDisconnect: a body cut off after n bytes gives those bytes,
//     then fails with ErrDisconnected, which errors.Is also matches to
//     io.ErrUnexpectedEOF; a shorter body ends as it would; a cut at 0
//     fails the first read.
//   - TestUnitBlobs: each injected error fails its call, Append after the
//     bytes are stored; with no error the upload commits on the store; the
//     store's own BeginUpload failure passes through.
//
// The NuGet push tests (TestAccPushRefusals, TestAccPushFaults), the
// router kit's form tests (TestAccReadFormDisconnect,
// TestAccReadFormStoreFailure) and the Terraform state service's
// TestUnitUploadFaults use the fakes too.
package fault
