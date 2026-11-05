// Package antpath matches paths against Ant-style patterns, the form
// Artifactory uses for permission target and repository include and exclude
// patterns, token scope paths, the pattern search and remote repositories'
// external dependency patterns: '/' separates segments, "**" matches any
// number of segments, '*' any run of characters within one segment and '?'
// exactly one character.
//
// # Role
//
// kernel layer: Ant-style path matching that access permission targets and
// artifacts repository patterns share. The rest of the kernel holds values
// with no behaviour beyond parsing; this package holds matching behaviour,
// and sits in the kernel so that artifacts code can match its repository
// patterns without importing access. It imports only the standard library.
//
// # Inputs
//
//   - Patterns as configured or requested: a leading '/' is ignored, and a
//     trailing '/' means everything below, so "dir/" is "dir/**".
//   - Paths relative to a repository root, '/'-separated; "" is the root.
//
// # Outputs
//
//   - CompilePattern, which turns a pattern into a Pattern.
//   - Pattern.Match, whether a pattern matches a path, and
//     Pattern.MatchBelow, whether it could match some path below a folder,
//     which is how a listing shows the folders that lead to permitted
//     content.
//
// # Invariants
//
//   - Matching follows Ant semantics and is case-sensitive (decision 0006).
//     Permission answers, repository pattern checks and searches depend on
//     it, so every match answer stays as it is.
//   - Match fills a table rather than backtracking, so a pattern with many
//     "**" segments cannot take exponential time on a path from a request.
//   - Segments are compared rune by rune, so '?' matches one character
//     rather than one byte of a multi-byte character.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Package placement and names" (kernel/antpath is a new package the
//     target layout does not name).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 118 (the kernel holds values with no behaviour beyond parsing) and
//     line 540 (pattern.go under access/core/authz); decision 0044 records
//     both deviations.
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md,
//     "Constraints" and "Implementation notes" (Ant semantics, the trailing
//     '/', folders that lead to permitted content).
//   - https://docs.jfrog.com/administration/reference/createorreplacepermissiontargetv2
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     ACC (https://github.com/weaveplatform/artifactweave/issues/940), and
//     #p4-identity-and-authorization.
//   - https://github.com/weaveplatform/artifactweave/issues/964
//
// # Status
//
// Implemented P4 with the native permission evaluator. Staged in P9d.
//
// # Testing
//
//   - TestUnitPatternMatch: the examples of Ant's pattern documentation,
//     which Artifactory's include and exclude patterns follow, and
//     Artifactory's usual forms.
//   - TestUnitPatternMatchBelow: the folders that lead to content a pattern
//     can match.
//   - FuzzPatternMatch, with inline seeds and no seed corpus directory:
//     Match agrees with a direct recursive matcher, and "**" matches every
//     path. make test runs the seeds (FUZZ_PKGS) and CI runs them in the
//     three-OS Go test matrix; go test -fuzz=FuzzPatternMatch explores
//     further.
package antpath
