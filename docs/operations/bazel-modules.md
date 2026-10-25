# Connect Bazel Modules to artifactweave

This recipe was verified with **Bazel 9.3.1** on 2026-10-25. The pinned
client image is `gcr.io/bazel-public/bazel@sha256:d090e62af5f0190bef545f278677bab7175d7db9c028d1e492bb4bfb3e1975fa`.
The [native acceptance test](../../internal/server/bazelmodules/bazelmodules_test.go)
builds a controlled module through local, remote, virtual and one-node
federated repositories. Cross-peer federation remains a separate P13 contract.

## Configure and publish

Create a `bazelmodules` local repository, and optionally a remote repository
pointing to `https://bcr.bazel.build` and a virtual repository with those
members in resolution order. A virtual repository needs a default deployment
repository for uploads. The native registry URL is:

```text
https://registry.example/artifactory/api/bazelmodules/bazel-virtual
```

Add the URL to the project `.bazelrc`:

```text
common --registry=https://registry.example/artifactory/api/bazelmodules/bazel-virtual
```

For a private registry, give Bazel a `~/.netrc` entry containing the
Artifactory host, user and an identity token. Restrict the file to the user
(`chmod 600 ~/.netrc`). Bazel reads this for registry requests and the
artifactweave archive URL returned in `source.json`.

Publish **both** the source archive and `MODULE.bazel` under
`modules/<name>/<version>/` with ordinary Artifactory upload or the JFrog CLI:

```sh
jf rt upload 'hello/1.0.0/*' bazel-local/modules/ --flat=false
```

The manifest must declare the same literal name and version as its path.
The archive can be `.tar.gz`, `.tgz`, `.tar` or `.zip`; its content must
match the suffix. A complete version appears in `metadata.json` only when
both files pass validation. artifactweave generates `source.json`, including
the archive URL, SHA-256 integrity and enclosing directory to strip. Do not
upload `source.json`. An archive is unique per version.

In the consuming project, declare the dependency and build its target:

```starlark
# MODULE.bazel
module(name = "consumer")
bazel_dep(name = "hello", version = "1.0.0")
```

```sh
bazel build @hello//:all
```

Bazel may need public modules for its own toolchain, such as `rules_cc`.
The acceptance fixture supplies Bazel Central Registry as a second registry
for those dependencies; an operator can instead put a BCR remote member in
the virtual repository.

## Remote source policy

A remote `bazelmodules` repository caches registry metadata and validated
archive bytes through the shared source service. `source.json` archive URLs
are rewritten to artifactweave, so Bazel does not fetch them directly from
the upstream host. Archive SHA-256 integrity is checked before cache
publication. Same-origin redirects are supported; an archive on another
origin needs an operator-configured external dependency allow pattern for
that exact destination and its redirects. This is relevant for BCR modules
whose archives are hosted on GitHub or another source host. Offline reads
use only previously cached metadata and archives.

Delete and restore change the native version view from the current item
tree. Format reindex validates every local publication without creating a
second mutable registry listing. Remote refresh follows the repository's
ordinary source-cache policy.

## Class evidence and upstream documentation

The current [JFrog Bazel Modules guide](https://docs.jfrog.com/artifactory/docs/bazel-modules-repositories)
describes local, remote and virtual repositories, while the current
[package-client class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients)
lists Bazel Modules as remote only. The signed
[inventory notes](../implementation/package-format-inventory-notes.md)
record that discrepancy. artifactweave's accepted repository configuration
includes L/R/V/F, and the controlled Bazel 9.3.1 proof covers those four
classes. [#216](https://github.com/weaveplatform/artifactweave/issues/216)
still owns comparison with the chosen live Artifactory target build.

Bazel Modules repositories serve module dependency resolution. Bazel's
action cache and Maven dependencies use their separate repository protocols,
as the JFrog guide describes.
