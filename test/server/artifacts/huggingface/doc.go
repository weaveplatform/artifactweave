// Package huggingface runs the Hugging Face Xet routes of the assembled
// server: reconstruction, capabilities, shard authentication and commit
// provenance, and the pinned native client.
//
// # Role
//
// Test support: server-assembly suites for the Hugging Face ecosystem,
// outside the modules. They drive the production router through HTTP
// requests. The package is one of its own so that CI selection can run it
// alone; its owner in tools/citestselection/test_ownership.json is
// huggingface.
//
// # Inputs
//
//   - The production router from test/server/support/harness, through the
//     harness's item world under the root's name (fixture_test.go,
//     newItemWorld): the users reader, deployer and user, the deploy-libs
//     permission and eight local, remote and virtual repositories, with the
//     request and seed helpers of test/server/support.
//   - The Xet fixtures in
//     migration/artifacts/ecosystem/huggingface/xet/testdata, read
//     root-relative through repopath.
//   - A Python with the pinned huggingface_hub and hf_xet named by
//     HF_XET_PYTHON; TestAccHuggingFaceNativeXet skips without it, and the
//     quality gate's Hugging Face step sets it. tools/depsync/catalog.json
//     keeps the client versions in that test current.
//
// # Outputs
//
// Assertions only:
//   - huggingface_xet_test.go and huggingface_xet_provenance_test.go:
//     published reconstruction and capabilities, corrupt content and generic
//     bypasses, shard declarations, oversized capabilities, and the caller
//     and content provenance of commits.
//   - huggingface_xet_native_test.go: the native Hub client's upload and
//     download through Xet.
//
// # References
//
//   - Decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md.
//   - docs/testing/huggingface.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md
//     (test/server/artifacts/<type> holds each ecosystem's server-assembly
//     suites).
//   - docs/testing/server-test-ownership.md: how CI selection gives the
//     package an owner.
//   - https://github.com/weaveplatform/artifactweave/issues/1068
//
// # Status
//
// Implemented P9c (the plan's P32 group). P9d moved the tests here from the
// server root (TST-06).
package huggingface
