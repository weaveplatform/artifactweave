# Package format inventory notes

Reviewed 2026-10-24 against the current published Artifactory 7.x guides. The
exact live target build is still an [oracle question](https://github.com/weaveplatform/artifactweave/issues/216).
The [machine-readable inventory](package-format-inventory.json) records the
classes currently accepted by `pkg/repo`, the client and required operations,
the route owner, metadata, lifecycle, native evidence or gap owner. The
[generated matrix](package-format-matrix.md) is the review surface.

## What the class letters mean

L, R and V are local, remote and virtual, as in JFrog's
[package-client class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients).
F is federated. The table does not cover F. A configured F repository can be
accepted by the API while the format's one-node client behavior remains a gap;
cross-peer convergence belongs to P13. Native completion requires a fresh client
on each approved class, not just a successful repository create or a generic
file upload. A source document without a fixed release number is dated here and
must be rechecked against the target build before changing a class boundary.

The 45 guide-catalog types plus four accepted types without a dedicated guide
page (`generic`, `ivy`, `helmoci`, `releasebundles`) make 49 registered types.
Terraform registry and backend are separate rows even though they share a
guide. The 28 `p9c` rows are the accelerated work; each changes from `gap` to
`native` when its client evidence lands. `releasebundles` remains
owned by P15/P24. The earlier native rows point to existing tests, not a new
claim that all P10/P13 class work has already landed.

## Published source differences to resolve against a live target

| Type | Current class table | Current format guide | Registered configuration | P9c boundary |
| --- | --- | --- | --- | --- |
| Bazel Modules | R only | [L/R/V](https://docs.jfrog.com/artifactory/docs/bazel-modules-repositories) | L/R/V/F | #772 proves L/R/V on a target build; F is a separate one-node observation. Do not infer native V from the table or F from either guide. |
| Opkg | L/R; V marked no | [L/R/V](https://docs.jfrog.com/artifactory/docs/opkg-repositories) | L/R/V/F | #760 proves L/R/V on a target build; F is a separate one-node observation. Do not label V unsupported solely from the table. |
| Agent Packages | L only | [format guide](https://docs.jfrog.com/artifactory/docs/agent-packages-repositories) | L/F | #216 checks whether F is accepted by the chosen target build; existing P9b evidence is local. |
| Machine Learning | Not in the package-client table | [Guide says ML is a repository structure, not a package type](https://docs.jfrog.com/artifactory/docs/machine-learning-repositories) | L/F under `machinelearning` | #780 must reconcile the current FrogML structure and the accepted configuration name against a target build before claiming a native type. |

The source table and individual guides conflict for Bazel and Opkg, so the
matrix shows the current registered class union while the native format issues
must record the observed version and client result. Until those results exist,
the extra class is a versioned question, not a delivered protocol. A target
build that rejects an extra class should narrow `pkg/repo` and update this
inventory in the same PR.

The [JetBrains plugin guide](https://docs.jfrog.com/artifactory/docs/jetbrains-plugins-repositories)
and [AI editor extension guide](https://docs.jfrog.com/artifactory/docs/ai-editor-extension-repositories)
describe remote, read-only marketplace proxies whose IDE gallery calls are
anonymous. Their #774/#775 client matrices must test that documented boundary;
generic authenticated package-client assumptions do not apply.

## Ownership and architecture

- [Decision 0003](../research/decisions/0003-repository-model.md) owns
  repository class and type validation; [decision 0032](../research/decisions/0032-package-format-handlers.md)
  owns admission, metadata, reindex and protocol routing.
- [Decision 0007](../research/decisions/0007-remote-repositories.md) and the
  P9 source/cache work own upstream transport, failure and refresh behavior.
- [Decision 0008](../research/decisions/0008-virtual-repositories.md) owns
  ordered member selection and denied-member handling; each P9c format owns
  its native virtual representation.
- [Decision 0010](../research/decisions/0010-retention-trash-and-gc.md) and
  [decision 0018](../research/decisions/0018-package-index.md) govern item
  lifecycle and indexed package metadata. [Decision 0015](../research/decisions/0015-testing-and-acceptance.md)
  requires real-client proof.

The CI command `go run ./tools/formatinventory -check` checks every registered
type and class against the inventory, requires an owner and per-class operation
entry for each gap, verifies route-owner and evidence files, and compares the
generated matrix. P20 will extend this format guard to the wider product API
and client-operation inventory.
