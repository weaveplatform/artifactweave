// Package authz evaluates Artifactory's permission model (decision 0006)
// for one subject: the admin flag, the union of grants to a user and its
// groups, permission targets with Ant include and exclude patterns, the
// anonymous user, and token scopes, which only ever remove actions but for
// applied-permissions/admin, an administrator's token whatever its subject.
//
// # Role
//
// access module, core layer: the native permission evaluator, which checks
// permission targets, token scopes and the admin rules for one subject. It
// is a library: the authenticator and the access handlers build a Decider
// per request with New and ask it questions. Other modules reach it only
// as the access/api Decider (AsAPI), which holds the questions they ask;
// the questions about permission documents and the subject's own grants
// (Holds, Grantable, Self and ManagesResources) and the evaluator's
// building blocks (Actions and RepoConfig) stay on the concrete Decider,
// which only access code imports. The package imports the standard
// library, the kernel and kernel/antpath, the module's api, core/model and
// core/port, and artifacts/api/repo for ResolveKey alone, which maps a
// remote repository's cache key to the repository a target names.
//
// # Inputs
//
//   - New: Sources (the permission and group stores, read once, and the
//     anonymous access setting) and the accessapi.Subject the request acts
//     as, with the scope of the token it carries, if any.
//   - ManagesResources: the user and group stores, whose role flags it
//     reads.
//   - FromAPI: an accessapi.Decider to get the evaluator back from.
//
// # Outputs
//
//   - *Decider, from New: the answers for one subject from one snapshot of
//     the permissions.
//   - AsAPI: the Decider as an accessapi.Decider, and FromAPI, the
//     evaluator behind one.
//   - TargetAllows: whether a permission target's include and exclude
//     patterns admit a path.
//
// # Invariants
//
//   - A token scope only removes actions: a scoped subject is never allowed
//     what the same subject without the scope is refused, except that
//     applied-permissions/admin acts as an administrator (decision 0005).
//   - New reads the permissions once, and the Decider answers from that
//     snapshot without writing, so one Decider may serve several goroutines
//     and a permission change applies from the next request's Decider.
//   - A permission target naming a repository that no longer exists never
//     matches, as Artifactory keeps such targets and ignores them, and a
//     key "<remote>-cache" is judged as its remote.
//   - Every error starts "authz: " and names the read that failed: the
//     subject's permissions, a user or a group.
//
// # References
//
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (token scopes only narrow).
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Public types and published api surface": Decider is an interface in
//     access/api; "Names the migration introduces", which lists AsAPI and
//     FromAPI).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 272 to 288 (access core, authz at line 279), line 540 (authz.go
//     with Decider and New) and line 990 (the package mapping row).
//   - Plan: docs/implementation/plan.md#p4-identity-and-authorization and
//     #p9d-modular-monolith-migration, epic ACC
//     (https://github.com/weaveplatform/artifactweave/issues/940).
//   - https://github.com/weaveplatform/artifactweave/issues/1063
//
// # Status
//
// Implemented P4: the evaluator with token scopes and the permission wire
// forms (pull request 92). Later changes limited a MANAGE holder's grants
// to the actions it holds (pull request 435), aligned the admin scope with
// 7.x (pull request 449), refused a MANAGE holder's changes to its own
// entries (pull request 459) and added the Manage Resources role (pull
// request 470). Moved here in P9d, which added AsAPI and FromAPI (pull
// request 1127).
//
// # Testing
//
//   - authz_test.go: TestUnitEvaluation (every evaluation rule over one
//     permission world, with and without token scopes),
//     TestUnitScopedConfigurationAndSystem, TestUnitScopeLettersMapToActions,
//     TestUnitNewReportsStoreFailures, TestUnitWireFormsEvaluateAlike,
//     TestUnitRepoVisibleAndGroups, TestUnitTargetAllows, TestUnitHolds,
//     TestUnitGrantable, TestUnitSelf and TestUnitManagesResources.
//   - api_test.go: TestUnitAsAPIAndFromAPI (the access/api view answers as
//     the evaluator does, and a nil or foreign Decider converts to nothing).
//   - username_test.go: TestUnitUsernameFolds (a subject named in another
//     case holds the grants of its folded name, #1432).
//
// The tests build the permission world on the access module's in-memory
// adapter.
package authz
