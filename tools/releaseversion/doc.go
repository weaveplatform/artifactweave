// Command releaseversion checks that a release's version reads the same in
// every place release-please and the release workflow take it from.
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary. PR Title Validation runs it with -title on the release-please pull
// request, and the release workflow runs it with -tag before it builds.
//
// # Inputs
//
//   - The version under check: the release pull request's title (-title) or
//     the pushed tag (-tag). Exactly one is given.
//   - Below the repository root (-root): .release-please-manifest.json,
//     CHANGELOG.md's newest heading, and the lines marked
//     x-release-please-version in the extra-files of release-please-config.json.
//
// # Outputs
//
//   - A failure that names each place whose version differs, and says to set
//     a version with a Release-As footer rather than by editing the title.
//
// # Invariants
//
//   - release-please tags the version in the merged pull request's title,
//     while the files it commits carry the version it computed. The two can
//     only disagree through a hand edit of the title, which is what tagged
//     v0.3.0 on a 0.2.1 commit (#1154) and left v0.4.0 and v0.3.1 on one
//     commit (#1624).
//
// # References
//
//   - https://github.com/weaveplatform/artifactweave/issues/1947
//   - release-please 17.6.0 build/src/strategies/base.js, buildRelease: the
//     version comes from PullRequestTitle.getVersion().
//
// # Status
//
// Implemented for issue 1947.
package main
