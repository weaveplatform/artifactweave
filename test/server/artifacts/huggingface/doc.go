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
//     internal/artifacts/ecosystem/huggingface/xet/testdata, read
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
//     bypasses, shard declarations, oversized capabilities, the caller and
//     content provenance of commits, and the download policy on resolve,
//     info, tree and Xet, directly and through a virtual repository.
//   - huggingface_xet_native_test.go: the native Hub client's upload and
//     download through Xet.
//   - huggingface_resolve_headers_test.go: a resolve's Hub headers and its
//     304, from every repository class (#1581).
//   - huggingface_lfs_checksums_test.go: a Git LFS file keeps its upload's
//     SHA-1 and MD5, in its item download and storage answer, and in a
//     revision that inherits it (#1581).
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
