# Dart pub native repository

Issue [#765](https://github.com/weaveplatform/artifactweave/issues/765) implements the
hosted pub v2 API at `/artifactory/api/pub/{repo}`. The current
[JFrog pub guide](https://docs.jfrog.com/artifactory/docs/pub-repositories)
and [class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients)
were checked on 2026-10-24. The [approved matrix](../implementation/package-format-matrix.md)
sets local, remote, virtual and one-node federated behavior in scope.
Cross-peer federation remains with P13.

## Connect Dart

Create a repository with `packageType: pub`. Use an HTTPS public base URL:
Dart refuses to save a token for an HTTP hosted URL. For a local or
federated repository, set:

```sh
export PUB_HOSTED_URL="https://registry.example/artifactory/api/pub/pub-local"
export AW_PUB_TOKEN="your-access-token"
dart pub token add "$PUB_HOSTED_URL" --env-var AW_PUB_TOKEN
```

Set `publish_to: https://registry.example/artifactory/api/pub/pub-local`
in the producer's `pubspec.yaml`, then run `dart pub publish`. A consumer
can use `PUB_HOSTED_URL` and `dart pub get`, or put the hosted URL on an
individual dependency. Set a remote repository's URL to the upstream hosted
registry, such as `https://pub.dev`.

A virtual URL aggregates its members as 7.x's package handlers walk them:
local and federated members before remote ones, members with
`priorityResolution` first, nested virtual repositories flattened, and a
member reached twice, or a cycle, skipped. The first member that lists a
version supplies it. The virtual repository itself takes no permission, as no
permission target can name one: the caller's read permission on each member
decides what it lists, and a member the caller cannot read, one without the
package, and a remote whose upstream fails are passed over. `dart pub
publish` to a virtual URL stores the archive in its default deployment
repository, local or federated, and needs write permission there only, as
[RTFACT-31048](https://jfrog.atlassian.net/browse/RTFACT-31048)'s log of a
publish through a virtual repository shows; without a default deployment
repository it answers 405 with `Allow: GET`.

Packages are stored at `<name>/<name>-<version>.tar.gz`; the server exposes
the package/version index through the hosted API. Upload checks the archive's
pubspec identity and canonical path, and each version is immutable. The
`POST /artifactory/api/pub/{repo}/reindex` operation rebuilds the local
index from stored archives. As 7.x's reference says, it needs an administrator
and queues the rebuild; `async=false` waits for it. A remote repository, or its
cache, answers 501, as no 7.x source says what its reindex rebuilds. Delete removes the version from the native view;
restore recreates it and its metadata.

## Verification

[`@pub` acceptance](../../test/acceptance/features/pub.feature) runs the
[digest-pinned Dart client](ci-client-images.md). It publishes and resolves on local and
one-node federated repositories, resolves local and remote packages through
a virtual repository and publishes through it to its default deployment
repository, follows a controlled remote archive redirect, and
resolves again from the server cache after the upstream stops and the client
cache is cleared. A remote refresh scenario zaps cached metadata and uses
`dart pub upgrade` to discover an added upstream version while retaining the
old one. A lifecycle scenario deletes, restores, reindexes and installs a
package. The client runs against a TLS front with a temporary test
CA installed in its container trust store; the entrypoint drops back to the
runner's UID before writing the client cache.

`internal/artifacts/ecosystem/pub` validates bounded gzip/TAR and YAML, safe archive
entries and package/version identity. `internal/artifacts/ecosystem/pub/http` filters remote
metadata by exact artifact read permission, checks current source authority
before delivery and validates downloaded archive identity and SHA-256. These
tests use controlled fixtures; they do not claim parity with an unobserved
live Artifactory build (#216).
