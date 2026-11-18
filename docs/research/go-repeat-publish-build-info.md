# Repeated Go publication and collected build-info

Testing issue [#1706](https://github.com/weaveplatform/artifactweave/issues/1706)
records this diagnosis. The `@go_virtual_publish` acceptance scenario failed in
PR #1698's native job 134955107943: a build contained four artifacts where the fixture expected three.
The extra entry was another `v1.0.0.info` with a different checksum. This is a
timing assumption in the fixture, rather than a demonstrated registry defect.

## Pinned client behavior

The repository pins JFrog CLI 2.131.0, whose
[module file](https://github.com/jfrog/jfrog-cli/blob/v2.131.0/go.mod#L18-L26)
selects `jfrog-cli-artifactory` at `30c80a7deec4` and `build-info-go` at
`f0cd26bfc2de`.

- [`createInfoFile`](https://github.com/jfrog/jfrog-cli-artifactory/blob/30c80a7deec4/artifactory/commands/golang/publish.go#L116-L139)
  writes the current time, with second precision, into `.info` on every publish.
  Two publishes of unchanged module sources can therefore upload different
  `.info` bytes.
- [`GoPublishCommand.Run`](https://github.com/jfrog/jfrog-cli-artifactory/blob/30c80a7deec4/artifactory/commands/golang/gopublish.go#L108-L138)
  adds each publication's artifacts to the selected build.
- [`addArtifactToPartialModule`](https://github.com/jfrog/build-info-go/blob/f0cd26bfc2de/build/build.go#L494-L501)
  indexes collected artifacts by path, SHA-1 and MD5. It retains both `.info`
  variants when commands share a build name and number. Identical `.mod` and
  `.zip` artifacts coalesce.

## Reproduction and attribution

The unchanged scenario failed on main
`38e8e4aec0d9c8d9179208f27c662908ad9167fc`, which does not contain PR #1698's
SQLite path change. Both Go publishes and build-info publication succeeded;
the three-entry assertion saw four.

A reduced diagnostic repeated exactly two publishes across a measured second
boundary. The captured `.info` timestamps were `2026-11-18T17:30:45Z` and
`2026-11-18T17:30:46Z`. Their SHA-1 values were
`412b8487a9259f74b9ad0bc9f4447871c2a91655` and
`24dbc778102942213198735fb7de524788277e85`. The CLI's build-info PUT already
contained four artifacts. Its `modules` array exactly matched the subsequent
server GET, so the registry did not invent or duplicate an artifact.

The existing [build-info contract](decisions/0014-build-info.md) permits a file
name with different content in one module; its duplicate-entry regression
separately covers that distinction. The Go contract also deliberately permits
[replacement of `.info`](decisions/0035-go-modules.md#deploys), while unchanged
`.zip` and `.mod` may be republished and changed bytes are refused.

## Fixture correction

Each publish now collects into a separate build number, and both published
builds must contain exactly three artifacts. The scenario still publishes the
same module version twice, verifies all three stored files, performs a cold
local-plus-private-remote dependency build, and refuses changed immutable ZIP
content with 409. It neither raises the artifact count nor retries assertions.

An ignored diagnostic overlay forced the second publish to start after the
first stored `.info` second. The complete corrected scenario passed all 55
steps, including both three-artifact assertions and the subsequent consumer and
conflict controls. The ordinary scenario has no added clock wait.

Local diagnostics used the existing Linux ARM64 Go 1.27.4 runner and the
repository-pinned Linux AMD64 JFrog CLI binary through the available binary
emulation. The binary's SHA-256 matched the CI pin,
`b19b2fe03efe2058f9be44f746a1eb83d469d31e67d2acd4093d7580c676f01f`.
The ordinary `@go` selection then passed all 10 scenarios and 286 steps with
zero skipped steps in 49.445 seconds. A first broader local run used runtime
temporary directories beneath the checkout, causing two no-`go.mod` cases to
inherit the repository module. That failed setup log was retained; the passing
run used disposable runtime directories outside any module, matching CI.
Native Linux AMD64 CI remains a separate qualification.
