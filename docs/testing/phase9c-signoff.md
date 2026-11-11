# Phase 9c combined qualification

All 28 format implementations and their follow-ups are qualified on merged main `4d9798d8895a4798f4afb570339bc0b2a46e973f`. The [full main run](https://github.com/weaveplatform/artifactweave/actions/runs/41790527538) and [current release-candidate run](https://github.com/weaveplatform/artifactweave/actions/runs/41790580588) passed on 2026-11-04. This closes the combined qualification in [#792](https://github.com/weaveplatform/artifactweave/issues/792), alongside [#810's CI evidence](phase9c-ci-signoff.md). P10 has not started.

The [format ledger](evidence/phase9c-formats.json) records all 28 completed rows and the exact qualified input hashes. The [retained run evidence](evidence/phase9c-final-runs.json) includes the selected-test inventories, coverage reports and policy, observed installed client versions, built-image identities, native execution results and artifact digests. Input hashes and runtime observations remain distinct.

## Format and client evidence

L/R/V/F mean local, remote, virtual and federated. F proves native behavior on one node. The [versioned inventory](../implementation/package-format-inventory.json) and its [rendered matrix](../implementation/package-format-matrix.md) remain the configuration contract. `TestUnitAllowedPackageTypes` explicitly covers refused classes, and the inventory guard compares every registered type/class with the catalog. Native publication, metadata, download and security evidence is separate from class admission.

The table links the implemented class, native-client and fault evidence exercised by the successful combined run. Each format has a [connection recipe](../client-recipes/index.md). The native Bazel, Eclipse P2, VS Code and JetBrains recipes require explicit anonymous READ grants; they do not prove private-user or token-authenticated native client support. Other recipes document their supported credentials. HTTP/API authorization tests and native-client authorization claims remain distinct.

| Format | Classes | Native and fault/lifecycle evidence | Official guide |
|---|---|---|---|
| [alpine](../client-recipes/alpine.md) | L/R/V/F | [Signed APK v2 indexes; cold/offline apk installs, redirect/integrity rejection and index lifecycle.](../../test/acceptance/features/alpine.feature) | [guide](https://docs.jfrog.com/artifactory/docs/alpine-linux-repositories) |
| [cargo](../client-recipes/cargo.md) | L/R/F | [Sparse publish/build, yank/unyank and checksum-pinned remote cache; virtual is refused.](../../test/acceptance/features/cargo.feature) | [guide](https://docs.jfrog.com/artifactory/docs/cargo-repositories) |
| [composer](../client-recipes/composer.md) | L/R/V/F | [Composer 2 installs and ordered virtual members; Composer 1 local compatibility; metadata/ZIP validation.](../../test/acceptance/features/composer.feature) | [guide](https://docs.jfrog.com/artifactory/docs/php-composer-repositories) |
| [bower](../client-recipes/bower.md) | L/R/V/F | [Native registry installs, persistent private Git/SSH registration, art resolver rewriting and archive validation.](../../test/acceptance/features/bower.feature) | [guide](https://docs.jfrog.com/artifactory/docs/bower-repositories) |
| [chef](../client-recipes/chef.md) | L/R/V/F | [Knife/knife-art cookbook publish/install/unshare with dependencies, remote redirects and offline cache.](../../test/acceptance/features/chef.feature) | [guide](https://docs.jfrog.com/artifactory/docs/chef-repositories) |
| [cocoapods](../client-recipes/cocoapods.md) | L/R/V/F | [CDN and HTTP/Git source installs, pinned recursive submodules, fresh offline cache and lifecycle.](../../test/acceptance/features/cocoapods.feature) | [guide](https://docs.jfrog.com/artifactory/docs/cocoapods-repositories) |
| [conan](../client-recipes/conan.md) | L/R/V/F | [Conan 2 and legacy Conan 1 revision publication/download; manifests, remote cache and incomplete-revision refusal.](../../docs/testing/conan.md) | [guide](https://docs.jfrog.com/artifactory/docs/conan-repositories) |
| [conda](../client-recipes/conda.md) | L/R/V/F | [micromamba channel installs, modern .conda archives, sharded metadata, refresh/offline and lifecycle.](../../test/acceptance/features/conda.feature) | [guide](https://docs.jfrog.com/artifactory/docs/conda-repositories) |
| [cran](../client-recipes/cran.md) | L/R/V/F | [Native R installs, remote redirects/offline, ordered virtual members and guarded archive migration.](../../test/acceptance/features/cran.feature) | [guide](https://docs.jfrog.com/artifactory/docs/cran-repositories) |
| [debian](../client-recipes/debian.md) | L/R/V/F | [Signed binary/source indexes and snapshots; apt installs, remote outage, member filtering and delete/restore.](../../test/acceptance/features/debian.feature) | [guide](https://docs.jfrog.com/artifactory/docs/debian-repositories) |
| [huggingfaceml](../client-recipes/huggingfaceml.md) | L/R/V/F | [Hub/CLI and Xet model/dataset transfers; authorized legacy migration, rollback/retry and incremental commits.](../../test/acceptance/features/huggingface_xet.feature) | [guide](https://docs.jfrog.com/artifactory/docs/hugging-face-repositories) |
| [gems](../client-recipes/gems.md) | L/R/V/F | [gem/Bundler installs, compact indexes, publish/yank and cached remote resolution.](../../test/acceptance/features/gems.feature) | [guide](https://docs.jfrog.com/artifactory/docs/rubygems-repositories) |
| [gitlfs](../client-recipes/gitlfs.md) | L/R/V/F | [Native push/fresh clone and offline object reads; two-user locks, conflicting pre-push rejection and remote lock freshness.](../../test/acceptance/features/gitlfs.feature) | [guide](https://docs.jfrog.com/artifactory/docs/git-lfs-repositories) |
| [opkg](../client-recipes/opkg.md) | L/R/V/F | [Native feeds and signatures; independent OpenWrt fixture, redirect/cache faults and ordered virtual members.](../../test/acceptance/features/opkg.feature) | [guide](https://docs.jfrog.com/artifactory/docs/opkg-repositories) |
| [pub](../client-recipes/pub.md) | L/R/V/F | [Dart publish/install/upgrade; fresh offline cache, virtual members and archive checksum rejection.](../../test/acceptance/features/pub.feature) | [guide](https://docs.jfrog.com/artifactory/docs/pub-repositories) |
| [puppet](../client-recipes/puppet.md) | L/R/V/F | [Forge module/dependency installs, remote redirects/offline, virtual dependencies and metadata/authority failures.](../../test/acceptance/features/puppet.feature) | [guide](https://docs.jfrog.com/artifactory/docs/puppet-repositories) |
| [rpm](../client-recipes/rpm.md) | L/R/V/F | [DNF installs and GPG repodata; remote refresh/offline, virtual SHA pinning and Yum reindex.](../../test/acceptance/features/rpm.feature) | [guide](https://docs.jfrog.com/artifactory/docs/rpm-repositories) |
| [swift](../client-recipes/swift.md) | L/R/V/F | [SwiftPM anonymous/authenticated publish on L/F and fresh remote/virtual/offline resolves.](https://github.com/weaveplatform/artifactweave/blob/ec4592a513985e4986f60b060984e40c3347be73/internal/server/swift/swift_test.go) | [guide](https://docs.jfrog.com/artifactory/docs/swift-repositories) |
| [vagrant](../client-recipes/vagrant.md) | L/F | [Native box add/update, provider selection and verified bytes; hosted lifecycle and invalid-token rejection.](../../test/acceptance/features/vagrant.feature) | [guide](https://docs.jfrog.com/artifactory/docs/vagrant-repositories) |
| [ansible](../client-recipes/ansible.md) | L/R/V/F | [Galaxy collection publish/install; ordered members, fresh offline install, checksum and credential denial.](../../test/acceptance/features/ansible.feature) | [guide](https://docs.jfrog.com/artifactory/docs/ansible-repositories) |
| [bazelmodules](../client-recipes/bazelmodules.md) | L/R/V/F | [Bzlmod builds through all four classes; exact fixture archive delivery, yanks and incomplete-publication refusal.](https://github.com/weaveplatform/artifactweave/blob/ec4592a513985e4986f60b060984e40c3347be73/internal/server/bazelmodules/bazelmodules_test.go) | [guide](https://docs.jfrog.com/artifactory/docs/bazel-modules-repositories) |
| [vcs](../client-recipes/vcs.md) | R | [Git archive/ref proxy; curl recipe in the Maven image, offline cache and STASH/OLDSTASH/ARTIFACTORY provider contracts.](../../test/acceptance/features/vcs_recipes.feature) | [guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories) |
| [p2](../client-recipes/p2.md) | R/V | [Eclipse director installs through remote/composite/virtual sites; external child origins and checksum refusal.](../../test/acceptance/features/p2.feature) | [guide](https://docs.jfrog.com/artifactory/docs/p2-repositories) |
| [hex](../client-recipes/hex.md) | L/R/V | [RSA-signed registry and Mix dependency resolution, legacy checksum compatibility and remote offline use.](../../test/acceptance/features/hex.feature) | [guide](https://docs.jfrog.com/artifactory/docs/hex-repositories) |
| [nix](../client-recipes/nix.md) | L/R/V/F | [Nix copy/substitute with Ed25519 trust, ordered virtual members, remote offline use and NAR integrity.](../../test/acceptance/features/nix.feature) | [guide](https://docs.jfrog.com/artifactory/docs/nix-repositories) |
| [machinelearning](../client-recipes/machinelearning.md) | L/F | [FrogML model publication/download; owned staging, immutable manifests and per-file authority/integrity.](../../test/acceptance/features/machinelearning.feature) | [guide](https://docs.jfrog.com/artifactory/docs/machine-learning-repositories) |
| [aieditorextensions](../client-recipes/aieditorextensions.md) | R | [VS Code extension installation through a remote proxy; VSIX/manifest/signature admission.](../../test/acceptance/features/ai_editor_extensions.feature) | [guide](https://docs.jfrog.com/artifactory/docs/ai-editor-extension-repositories) |
| [jetbrainsplugins](../client-recipes/jetbrainsplugins.md) | R | [Native IDE installation through a remote Marketplace proxy; redirect, metadata and archive checks.](../../test/acceptance/features/jetbrainsplugins.feature) | [guide](https://docs.jfrog.com/artifactory/docs/jetbrains-plugins-repositories) |

## Security, integrity and lifecycle boundaries

The full Go unit and package acceptance lanes include each adapter's protocol and format suites, the shared source/cache/authentication suites, and the root server/application integration tests. Native feature files exercise real clients; Go fault tests inject errors that clients cannot reliably produce themselves. The retained selected-test inventories include both kinds and the four current fuzz seed entry points.

- Denial includes metadata visibility, artifact reads, deployment/overwrite, current virtual-member authority and revocation before delivery. Git LFS adds owner-aware locks; Hugging Face and FrogML add transaction/staging ownership.
- Metadata and archive admission check bounds, path/package identity, digests and native signatures where the protocol supplies them. A malformed or truncated upstream response must not become a successful cache entry.
- Remote formats cover cold reads, refresh/redirect failures and fresh-client offline reuse. Read-only formats use cache lifecycle; hosted delete/restore/reindex is inapplicable to classes they reject. Remote Git LFS locks intentionally remain fresh upstream operations and fail during an outage.
- Virtual proof follows the configured member order and permission boundary. It applies only to rows containing V. One-node F proof applies only to rows containing F; neither implies cross-peer convergence.
- The late follow-ups are included: Git LFS locking #843, legacy VCS #844, Hugging Face migration #789 and current Hub/Xet #862/#863/#866, Bower #768, and connection recipes #791. Migration fault tests check rollback, source preservation and safe retry.

## Harness isolation review

Reviewed on 2026-11-03. `runContainerWith` gives each scenario its own `/work` home and removes containers after use. Host networking permits controlled upstream fixtures and tool bootstrap downloads; it is not an egress isolation guarantee. Qualification therefore requires evidence that the package under test came through artifactweave.

| Spot-check | Evidence preventing a warm-cache or direct-upstream claim |
|---|---|
| Bower | The configured registry is artifactweave; controlled archive markers are checked. Offline scenarios remove installed components and Bower caches before reinstalling. Private Git fixtures record the SSH/provider path. |
| CocoaPods | Podfile declares the artifactweave CDN source; `.cocoapods`, downloaded pods and cache state are cleared for independent installs. Rewritten archive URLs, controlled content and upstream request counts establish the delivery route. |
| Git LFS | The producer deletes `.git/lfs/objects` before cloning. The new clone's bytes must match the expected SHA-256 and `git lfs env` must name the artifactweave endpoint. |
| Hex | The dependency names its configured registry and key; `HEX_HOME`, dependency and build caches are cleared for the offline scenario. Ecto and transitive dependencies resolve through the same proxy. |
| SwiftPM | Temporary consumers and explicit cache paths are used; the fixture counts archive requests through each remote/virtual endpoint and checks no new upstream request after offline transition. |
| Bazel | Each class uses a new container/output root. BCR remains available for Bazel support modules, but success additionally requires artifactweave to serve the exact controlled module archive through that class. The client version is read from the pinned executable. |
| Conan | Both client versions clear recipe/package cache before downloads; the production-source remote test uses a controlled upstream and verifies the downloaded revision. |

The strengthened Bazel assertion passed through all four approved classes, including exact controlled archive delivery. Missing Docker after opting into that native test is a failure. The retained verbose results show native execution, including the yanked-version case.

## Client identity and compatibility lanes

The generated [image inventory](ci-client-images.md) records immutable base OCI digests and declared client pins. The [runner-installed inventory](ci-native-client-inventory.md) links the archive/wheel checksum sources. The qualified snapshot retains the corresponding Dockerfile, action, workflow and native fixture hashes through each recipe's `version_sources`. Bazel's direct OCI pin is in its native fixture and recipe harness rather than a Dockerfile. The core native job retains `native-client-identities-core`: full Docker image IDs/repository digests and the successful image-build log. Images print executable or installed SDK versions, including the curl inside the Maven image used by VCS. Eclipse and IntelliJ retain installed product version/build metadata from their checksum-verified distributions; their native scenarios separately prove execution. Bazel's verbose test logs its actual version. The retained run evidence records these artifact identities alongside runner-installed client version/checksum logs; a base image digest alone does not pin later package installation steps.

Purposeful compatibility lanes remain explicit: Conan 1 accompanies Conan 2; Composer 1 covers local repositories during its documented support window; Puppet uses the publicly available open-source client with its supported Ruby runtime. FrogML's Python bound differs from Ansible's supported Python runtime, so those images intentionally differ. Conda protocol proof uses micromamba. Historical qualification reports keep their original versions; the generated inventories describe current committed pins.

Current primary pins were rechecked against official release feeds on 2026-11-03. The [client release review](phase9c-client-release-review.md) records updates, source dates, checksum provenance and supported-runtime exceptions. Dependency generation updates current recipe labels, assertions and evidence locks; historical reports keep their original versions.

## Official-guide comparison

The 28 linked primary JFrog guides and the [repository support table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients) were checked on 2026-11-03. This review describes the published Artifactory 7.x contract; live target-version parity remains [#216](https://github.com/weaveplatform/artifactweave/issues/216).

The specialized [Bazel](https://docs.jfrog.com/artifactory/docs/bazel-modules-repositories) and [Opkg](https://docs.jfrog.com/artifactory/docs/opkg-repositories) guides describe local/remote/virtual behavior more broadly than the summary table. The existing approved matrix retains those classes. The summary table omits F, so one-node F claims rely on the registered contract and explicit native tests. Cargo remains L/R/F, Hex L/R/V, P2 R/V, Vagrant and Machine Learning L/F, and VCS/IDE proxies R only.

The [Machine Learning guide](https://docs.jfrog.com/artifactory/docs/machine-learning-repositories) describes the model storage structure separately from an Artifactory package type; the existing `machinelearning` configuration contract and FrogML protocol are retained, with external ML service roles/actions excluded from this phase. [Hugging Face](https://docs.jfrog.com/artifactory/docs/hugging-face-repositories) and the [IDE proxy](https://docs.jfrog.com/artifactory/docs/ai-editor-extension-repositories) guides also advertise scanning/Curation integrations. Native repository support here does not claim those later security services.

## Explicit handoffs

| Owner | Work retained after P9c |
|---|---|
| P10 | Shared virtual repository policy and the original-family virtual backlog. Adopt the narrow resolvers delivered for approved P9c formats without counting those implementations twice. |
| P13 | Peer trust, replication, cross-peer convergence, recovery and conflict behavior. One-node F is the boundary of this phase. |
| P20 | Wider operation/client/format inventory and automated unmapped-gap tracking; target-specific captures retain #216/P19 ownership. |
| P23 | General Evidence/provenance, attestation lifecycle, signing-key APIs and platform verification. Format-native package signatures are already part of their protocol evidence. |
| P27 | Advanced Security, Curation, package admission/rerouting and model/extension scanning integrations. |
| P39 | General browser console and repository/identity/governance/operations workflows. CLI recipes do not imply a web UI. |

## Final run record — 2026-11-04

| Run | Exact commit | Coverage | Selected tests |
|---|---|---|---|
| [main](https://github.com/weaveplatform/artifactweave/actions/runs/41790527538) | `4d9798d8895a4798f4afb570339bc0b2a46e973f` | 95.0% (74,201/78,106) | 3,726 |
| [release](https://github.com/weaveplatform/artifactweave/actions/runs/41790580588) | `36f4e05f6d9def3e5f96c91cb445dfbca62a0e5e` | 95.0% (74,203/78,106) | 3,726 |

Both exact-commit inventories match independent planner reproduction: 2,072 unit entries, 1,650 acceptance entries and 4 fuzz seed entry points. Required three-OS race/shuffle unit tests, Go package acceptance, native partitions, Terraform provider/deployment lanes, OCI, lint, vulnerability scanning, spec parity and cross-compilation passed. Gradle remains a separately labelled informational lane. Coverage is merged from each run's own artifacts; both the 95% overall threshold and every enforced 90% package floor passed without changing policy.

The final main run passed on attempt 1, and the release run passed on attempt 1. Earlier main `4315ebb81f05b290f7af93c8684230c778f98dd2` failed twice in [run 41774005652](https://github.com/weaveplatform/artifactweave/actions/runs/41774005652) before VS Code protocol operations: the sequential image-build queue exceeded the client's ten-minute readiness wait. Merged [#931](https://github.com/weaveplatform/artifactweave/pull/931) makes acceptance wait for successful image-build completion before launching tests, preserves bounded deadlines and failure reporting, and adds a regression that fails on the old ordering. The final runs above include that correction. Earlier failures remain diagnostic evidence and are not counted as successful qualification.

All 137 format input/evidence hashes match the qualified main commit. Runtime records cover all 25 Docker-backed P9c client rows, plus separate Conan, Hub/Xet and Bazel observations. Retained direct-native passes include migrated Hugging Face revisions, fresh-cache Xet, legacy VCS and Bazel across L/R/V/F. Core Godog and Conan use non-verbose package output: successful package/step completion is retained without inventing per-scenario execution counts.

[#810's ordinary selective probes](phase9c-ci-signoff.md) supply the ordinary-PR side of the four-lane policy; the release record above supplies the current complete candidate. Live vendor-oracle probes, real-cloud deployment approval, cross-peer federation and the later-phase handoffs remain outside this sign-off.

The original 94.9% coverage prerequisite was repaired by merged [#925](https://github.com/weaveplatform/artifactweave/pull/925). The current-client/platform and runner-disk corrections are included through #927/#930. [#918](https://github.com/weaveplatform/artifactweave/pull/918) repaired renamed evidence paths and completed dependency-reference automation; its live post-merge recovery and idempotent repeat both passed. Historical reports keep their original versions and tested commits.

This documentation commit records qualification of the implementation SHA above. Subsequent documentation-only sign-off edits do not relabel the executed run as testing another commit.
