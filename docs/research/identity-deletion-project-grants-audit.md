# Identity deletion and project grants audit

Read: **2026-11-19**. Target: current public Artifactory 7.x documentation; this
does **not** claim a capture against 7.161.32 or another vendor instance. Bounded
primary-source check for P11 / [#487](https://github.com/weaveplatform/artifactweave/issues/487).

| Label | Meaning |
|---|---|
| **[D]** | Explicit first-party documentation; the text below distinguishes REST references from UI/CLI guides. |
| **[S]** | Behavior visible in the cited source; client source proves client behavior, not server internals. |
| **[I]** | Inference or artifactweave implementation choice; not established vendor behavior. |

## Finding

**Public evidence checked here does not establish whether retaining a deleted
user's permission-target entries is intentional compatibility or a vendor defect.**
It also does not establish global-identity deletion cascades into project
memberships, or what either user or group same-name recreation restores. Preserve
those distinctions: a documented user-token rule is not a group lifecycle rule,
and deleting project membership is a separate operation from deleting an identity.

| Concern | User deletion | Group deletion |
|---|---|---|
| REST guarantee | **[D]** `DELETE /access/api/v2/users/{username}` deletes the existing named user; admin required; success is 204. No project, permission-entry, token, or recreation postcondition is specified. [Delete User](https://docs.jfrog.com/administration/reference/deleteuser) | **[D]** `DELETE /access/api/v2/groups/{name}` deletes the existing named group; admin required; success is 204. The same cascade/recreation postconditions are absent. [Delete a Group](https://docs.jfrog.com/administration/reference/deletegroup) |
| Project memberships | **Unsettled.** Neither identity-delete reference guarantees removal of the user's project assignments. | **Unsettled.** Neither identity-delete reference guarantees removal of the group's project assignments. |
| Permission-target entries | **Unsettled.** The user-delete reference neither promises removal nor documents intentional retention. | **Unsettled.** The group-delete reference neither promises removal nor documents intentional retention. |
| Tokens | **[D, guide/config]** The Identity Tokens guide says deletion or disabling revokes that user's identity tokens. The Access configuration reference exposes `token.delete-with-user: true`, explicitly controlling deletion of user tokens with the user. This supports the default cleanup policy, not an unconditional guarantee for every token/configuration. [Identity Tokens](https://docs.jfrog.com/user-management/docs/identity-tokens), [Supported Access Configurations](https://docs.jfrog.com/installation/docs/supported-access-configurations) | **Unsettled.** These user-specific statements do not promise that deleting a group revokes every token whose scope names it. The group-delete REST reference gives no such guarantee. |
| Same-name recreation | **Unsettled.** No checked source specifies restoration or non-restoration of old permissions/project assignments, or a fresh identity-incarnation boundary. | **Unsettled.** No checked source specifies these outcomes for a recreated group. Do not transfer a user rule to groups. |

**[D, UI guide]** The current [Manage Users](https://docs.jfrog.com/administration/docs/manage-users-2)
and [Manage Groups](https://docs.jfrog.com/administration/docs/manage-groups) pages
describe creation/editing and group assignment. Neither supplies a deletion or
same-name recreation contract. Their UI descriptions therefore add no cascade
guarantee to the REST references above.

**[D, CLI guide; narrower evidence]** The current
[`jf rt users-delete` guide](https://docs.jfrog.com/artifactory/docs/jf-rt-users-delete)
explicitly says users are not removed from groups and advises separate membership
cleanup. It also says artifacts/builds remain while account credentials are
removed. This is a statement about the documented CLI workflow, not a documented
Access v2 REST postcondition. It does not settle permission-target entries or
project memberships. Its group-membership wording is an additional reason not to
infer a universal cascading-delete rule.

## What first-party clients establish

**[S]** JFrog Go client's
[`UserService.DeleteUser`](https://github.com/jfrog/jfrog-client-go/blob/master/artifactory/services/users.go#L139)
and
[`GroupService.DeleteGroup`](https://github.com/jfrog/jfrog-client-go/blob/master/artifactory/services/groups.go#L144)
send one legacy `DELETE /api/security/users/{name}` or
`DELETE /api/security/groups/{name}`, with no request body, and require 200. Neither
method enumerates or edits permissions, projects, or tokens, nor asserts their
post-delete state. Source read from public `master` on the date above; this is not
a pinned server-version guarantee.

**[I]** Those clients leave any server-side cascade to the server. Their absence
of cleanup does not prove that the server intentionally keeps grants, or that it
removes them. No checked first-party source or targeted documentation search
established a 7.x defect/fix for stale permission entries after delete/recreate.
This is a bounded unresolved result, not a claim that no such defect exists.
No historical OSS source was used to fill the 7.x gap.

## Consequence for artifactweave P11

**[S, local context]** At service checkpoint `0b5b36241`, the existing
[`identity.Service`](../../internal/access/core/identity/identity.go) user deletion
folds the username, revokes matching tokens and deletes the user; group deletion
also removes that group's grants from every permission. Existing identity-store
deletion removes group membership, but leaves project membership rows. Those
local behaviors are not evidence of vendor behavior.

**[I, explicit P11 invariant]** A deleted principal must lose its project role
assignments, so a later same-name identity cannot regain them. The root regression
work confirmed stale project assignments for both user and group recreation when
activating project role lookup. P11's identity cleanup addresses that introduced
activation gap; it is not presented as a documented JFrog REST cascade.

The separate possible pre-existing **user permission-target grant** issue remains
unconfirmed: this audit provides neither a multi-resource-kind local reproduction
nor a vendor contract requiring removal. Do not call retention intentional,
declare a defect, or file a cross-kind issue on source absence alone. A future
vendor comparison should check users and groups independently across artifact,
build, release-bundle and other permission sections; direct and inherited project
membership; token configuration/scope; and same-name recreation.

This note does not narrow #487 or mark project authorization complete. Its full
scope, including the missing sharing-mode wire API, remains outstanding where not
implemented. No production files, issues, or vendor state were changed by this
research.
