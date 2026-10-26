# AI editor extensions remote repositories

`aieditorextensions` serves a read-only VS Code gallery through a remote
repository. The supported class is **R**. Configure the repository's upstream
URL to the gallery base, for example
`https://marketplace.visualstudio.com/_apis/public/gallery`. Configure VS
Code's `extensionsGallery.serviceUrl` to
`https://<registry>/artifactory/api/aieditorextensions/<repo>/_apis/public/gallery`.
Do not append `/_apis/public/gallery` to the repository URL a second time.

The IDE does not supply Artifactory credentials with its gallery calls. Enable
global anonymous access with an administrator configuration PATCH and grant the
anonymous principal **READ** on the intended extension path. The gallery route
still applies repository permissions to every result; a denied principal gets
no extension metadata or install asset. An exact extension query is scoped to
`extensions/<publisher>/<name>`; a broader search checks repository folder
access before fetching metadata, then filters each result by its own read
permission. Example administrator operations:

```http
PATCH /artifactory/api/system/configuration
Content-Type: application/yaml

security:
  anonAccessEnabled: true
```

```http
POST /access/api/v2/permissions
Content-Type: application/json

{"name":"editor-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"editor-remote":{"include_patterns":["extensions/ms-vscode/hexeditor/**"]}}}}}
```

The native POST is restricted to `extensionquery`. The source cache identifies
the canonical bounded JSON request by SHA-256; distinct queries do not share
catalog entries. The remote source validates publisher, extension, version,
file type and upstream origin before publication, then rewrites gallery asset
URLs to the local repository route. It serves the official manifest, VSIX and
signature bytes unchanged. The VSIX SHA-256 must match the gallery claim, and
the signature manifest's package digest must match the same VSIX claim. VSIX
and signature ZIP structure and size are bounded. Upstream redirects from the
gallery POST are rejected. Caller headers and credentials are never sent
upstream; configured remote credentials may be used for the upstream request.

Gallery and asset entries use the shared remote cache and honor its refresh,
offline, deletion and reindex behavior. The
[native VS Code acceptance](../../test/acceptance/features/ai_editor_extensions.feature)
uses pinned VS Code 1.140.0 and unchanged, checksum-verified Microsoft Hex
Editor 1.11.1 assets for cold and offline installs. The
[package matrix](package-format-matrix.md) records the R-only class boundary.
The client image uses digest-pinned Ubuntu 26.04 LTS: the same pinned VS Code
binary installs Hex Editor directly from Microsoft's gallery with signature
verification on 26.04 arm64, while the direct install fails signature
verification on 26.10 arm64 without artifactweave in the path. The VSIX,
signature asset and verification remain unchanged; [#852](https://github.com/weaveplatform/artifactweave/issues/852)
tracks qualification of a newer base.

Source: [JFrog AI Editor Extension repositories](https://docs.jfrog.com/artifactory/docs/ai-editor-extension-repositories).
