# Contributing

Thanks for considering a contribution. Please follow the
[code of conduct](CODE_OF_CONDUCT.md) in all interactions.

- **Issues.** File bugs and feature requests with the issue templates.
- **Pull requests.** Titles follow [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `docs:`, `chore:` …); release-please builds the changelog and
  the version from them, and a check enforces the format.
- **Before pushing,** run `make gate`. CI runs the same targets and the quality
  gate is the required check.

## Pull requests

Use [PR #1644](https://github.com/weaveplatform/artifactweave/pull/1644) and the
[PR template](.github/PULL_REQUEST_TEMPLATE.md) as the description structure:

1. **Summary:** the concrete problem, resulting behavior and implementation choices.
2. **Before and after:** reproducible baseline/branch evidence and unchanged controls.
3. **Answers kept on purpose:** relevant behavior deliberately retained, with reasons.
4. **Found while fixing:** every additional related bug and how it was handled.
5. **Tests:** actual verification commands, results, coverage scope and limitations.

Keep these sections even when a section's answer is "None". Scale the detail to
the change. Link the originating issue and primary sources near the claims they
support, and distinguish local results from pending CI.

For additional related bugs discovered during the work:

- **Same artifact kind:** resolve the bug in the current PR, even if it was not
  named in the original ticket. Include the reproduction, fix and regression
  evidence in **Found while fixing**.
- **Multiple artifact kinds:** automatically open a separate GitHub issue without
  asking for confirmation. Record the affected kinds, reproduction, expected and
  actual behavior, shared cause and acceptance criteria; link it from the PR and
  explain the separation. Reuse an existing issue only when it covers the same
  defect, and explain that match rather than creating a duplicate.
- Determine scope from affected behavior, not file location. NuGet V2 and V3
  are one artifact kind; a shared cache defect affecting NuGet and Docker is
  cross-kind. Verify that behavior is a defect before filing it; intended
  compatibility behavior belongs in **Answers kept on purpose**.

Fix regressions introduced by the PR within that PR. The separate-issue rule
for additional cross-kind defects does not permit introducing a regression.
