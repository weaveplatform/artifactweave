# CocoaPods CDN client and qualification

Phase 9c [#771](https://github.com/weaveplatform/artifactweave/issues/771)
provides the native CocoaPods CDN path for local, remote HTTP-source,
virtual, and one-node federated repositories. The current
[JFrog CocoaPods guide](https://docs.jfrog.com/artifactory/docs/cocoapods-repositories)
and [class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients)
were checked on 2026-10-25. The retired CocoaPods Git **index** is replaced by
CDN indexing. Git-sourced pod content and submodules remain in the Phase 9c
NAT-07 follow-up until its Git mirror and offline native-client proof land. A Git-source spec
currently fails closed instead of sending the client straight to its provider.

## Connect `pod`

Create a repository with `packageType: cocoapods` and the approved class.
For local or one-node federated publication, upload an archive at:

```text
PUT /artifactory/<repo>/<Name>/<version>/<Name>-<version>.tar.gz
```

The tar.gz needs a root `<Name>.podspec.json` or a root static
`<Name>.podspec` and the files its `source_files` selector references. The
podspec identity must equal the path. The server verifies the compressed
stream, archive paths and member types before it indexes the version. The
generated CDN podspec points `source.http` back to the checked archive with
its SHA-256. Delete, restore, and
`POST /artifactory/api/cocoapods/<repo>/reindex` rebuild the local view.

Add the CDN to a clean CocoaPods client home:

```sh
pod repo add-cdn aw https://<host>/artifactory/api/pods/<repo>
```

Put the same URL in the Podfile's `source` declaration. Use an authenticated
HTTPS connection for private repositories; the acceptance client uses a
per-scenario `.netrc` entry and never embeds a password in the Podfile.
Run `pod install --no-repo-update` after adding the source, or `pod install`
to refresh its CDN metadata.

For an HTTP-source remote, set `url` to the upstream source origin and
`podsCdnUrl` to its CDN metadata base. ArtifactWeave reads the CDN index and
spec through its remote source policy, validates the selected HTTP archive,
caches both, and rewrites `source.http` to its own route. An allowed source
archive can be `.tar.gz`, `.tgz`, or `.zip`; the source URL and checksum are
bound to its cache revision. Set `externalDependenciesEnabled: true` and
an `externalDependenciesPatterns` entry for each approved HTTP source URL;
the same URL patterns apply to archive redirects. For each hop, the adapter's
pattern check and the shared transport's DNS/address policy must both pass.
The transport strips origin credentials on a cross-origin redirect. A private
destination also needs an operator-approved private origin in the source
transport; a URL pattern alone does not permit private-network access. A
cross-origin HTTP archive is denied if `allowAnyHostAuth` or cookie management
could forward upstream credentials. An offline remote serves previously cached
metadata and source bytes to a fresh client. A virtual repository lists
members in priority order and keeps a selected package's metadata and bytes
with that member. Federation here is one-node behavior; cross-peer
convergence is [P13](../implementation/plan.md).

## Static Ruby podspecs

Uploaded Ruby is **parsed as data and never executed**. The accepted form is
`Pod::Spec.new do |s|` followed by one literal assignment or `s.dependency`
per line, with bounded nested static `subspec`, `test_spec` and `app_spec`
blocks and matching `end` lines. Literal values are quoted strings, symbols,
booleans, arrays, and simple hashes; literal arrays and hashes may span lines
and have a trailing comma. The parser maps `s.platform = :ios,
'12.0'` and platform `deployment_target` assignments into the JSON
`platforms` field. It accepts common identity, summary, author, license,
source, source file, resource, framework, library, and Swift-version fields,
including platform-qualified literal assignments. Unknown statements,
variables, interpolation, other method calls, dynamic blocks,
and shell commands are rejected before publication. The JFrog guide also
excludes dynamic podspec variables. Use `.podspec.json` for a podspec that
needs a static construct outside this conservative Ruby grammar.

## Evidence

The [pinned client image](../../test/acceptance/clients/cocoapods.Dockerfile)
installs `cocoapods` 1.17.0 with Ruby 3.3.6. The
[`@cocoapods` scenarios](../../test/acceptance/features/cocoapods.feature)
exercise actual `pod repo add-cdn` and `pod install` with a fresh home across
local JSON, local static Ruby, HTTP-source remote cache/offline and
same-origin redirects, local-backed
virtual, remote-backed virtual, and one-node federated repositories. The
controlled upstream fixture is authored independently of the server's CDN
encoder. Negative unit and server checks cover path identity, unsafe archive
members, dynamic Ruby, malformed source metadata and denied source paths. A
separate controlled transport test proves an approved cross-origin redirect to
a distinct archive host, private-origin denial without operator approval,
URL-pattern denial, and credential isolation.
The pinned-client cross-origin scenario starts the server with the exact
archive origin in `remote.allowed_private_origins` and proves the redirected
archive is installed without forwarding the configured upstream credentials.
