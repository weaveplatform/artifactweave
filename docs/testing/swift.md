# Swift Package Registry

SwiftPM uses `/artifactory/api/swift/<repository>` for local, remote, virtual and one-node federated Swift repositories. Local and federated publication stores an immutable source archive at `<scope>/<name>/<name>-<version>.zip`; the registry derives version lists, release metadata and manifests from that archive. Remote repositories cache validated registry responses and checksum-pinned ZIPs. Virtual repositories search members in configured order.

## Connect SwiftPM

From a Swift package directory, configure the HTTPS URL of the repository used for resolution. A virtual repository is useful when packages come from both local and remote members:

```sh
swift package-registry set "https://registry.example/artifactory/api/swift/swift-virtual"
```

Give SwiftPM an Artifactory user and token through a mode-0600 `~/.netrc` entry:

```text
machine registry.example login USERNAME password TOKEN
```

Alternatively, use `swift package-registry login` with the same HTTPS repository URL. SwiftPM's normal registry protocol requires TLS. The local test harness alone opts into loopback HTTP with `--allow-insecure-http`.

To publish from a package root to a **local or federated** repository:

```sh
swift package-registry publish acme.Widget 1.0.0 \
  --url https://registry.example/artifactory/api/swift/swift-local
```

To resolve from a project that declares `.package(id: "acme.Widget", from: "1.0.0")`:

```sh
swift package resolve
```

For Git URL dependencies that you want SwiftPM to replace with registry identities, use `swift package --replace-scm-with-registry resolve`. Direct `.package(id:from:)` dependencies resolve through the configured registry already. Xcode's Git URL dependencies need to be changed to registry IDs in the project.

## Native operations

The registry serves `GET /<scope>/<name>` (release list), `GET /<scope>/<name>/<version>` (metadata and SHA-256 source checksum), `GET /<scope>/<name>/<version>/Package.swift` (manifest), and `GET /<scope>/<name>/<version>.zip` (source archive). `PUT /<scope>/<name>/<version>` accepts SwiftPM's multipart publication with a `source-archive` part. A second publication of the same version is refused with 409. The generic item DELETE and trash restore paths remove and restore indexed releases. Administrators can queue a local or federated index rebuild with:

```sh
curl -u USERNAME:TOKEN -X POST \
  https://registry.example/artifactory/api/swift/swift-local/reindex
```

The archive validator checks one package root, the manifest, every ZIP member's path and CRC, and compressed and expanded size limits. A source archive over 512 MiB is refused with 413; any other archive that is not a valid package, an empty one included, is refused with 422. Remote release lists filter versions against exact archive-path read permission. Cached remote bytes remain usable in offline mode after the source has been fetched.

Signed publication is refused until archive signature validation is implemented; unsigned SwiftPM releases are accepted. The optional `/identifiers?url=` lookup returns `404` when no mapping is recorded.

Every error under `/artifactory/api/swift/<repository>/`, whether the registry, authentication or the request limiter refuses the request, is RFC 7807 problem JSON with `Content-Version: 1`. SwiftPM shows an error's `detail` only when that header is present, for example `failed publishing: server error 409: Swift release 1.0.0 already exists with different content`; without it SwiftPM prints its own text for the status. The `reindex` operation answers in Artifactory's error format instead.

## Evidence

- [`swift.feature`](../../test/acceptance/features/swift.feature) exercises native metadata, reindex and an error's `Content-Version` against the full server.
- [`TestAccSwiftPMAnonymousPublish` and `TestAccSwiftPMRemoteVirtualAndOffline`](../../internal/artifacts/ecosystem/swift/http/swift_test.go) run a real SwiftPM client against local, federated, remote and virtual repositories, with redirect and offline cache checks. CI uses the [digest-pinned Swift image](ci-client-images.md); local opt-in runs the installed SwiftPM client.
- [`TestAccSwiftPMPrintsProblemDetails`](../../internal/artifacts/ecosystem/swift/http/problem_test.go) runs SwiftPM, configured as the client recipe configures it, against the production router and checks that it prints the server's detail for a 401, a 403 and a 409. `TestAccSwiftErrorsCarryContentVersion` in the same file checks the header on the handlers', the authentication middleware's and the request limiter's errors.
- [JFrog Swift repository guide](https://docs.jfrog.com/artifactory/docs/swift-repositories) and the [Swift Package Registry specification](https://github.com/swiftlang/swift-package-manager/blob/main/Documentation/PackageRegistry/Registry.md) provide the client and protocol contracts.

Guide and specification checked on 2026-10-25. The local real-client evidence used SwiftPM 6.4; the Linux CI image at that time pinned SwiftPM 6.0.3. The error handling was checked on 2026-11-12 against the specification's sections 3.3 to 3.5 and `RegistryClient.unexpectedStatusError` in SwiftPM 6.4.2.
