# 0013: Projects and environments

Status: Proposed

Checked against: Artifactory 7.161.32

Project-aware sharing introduction checked separately against 7.173.4 SaaS and
7.173.8 self-managed; see the [current contract audit](../project-scoped-remotes-contract-audit.md).

## Context

JFrog projects group repositories, members and roles under a key that prefixes resource
names, with storage quotas and project-scoped admin privileges. Environments (`DEV` and
`PROD` built in, plus custom ones) tag repositories and are mandatory when a repository is
assigned to a project. The `jfrog/project` Terraform provider manages all of it and polls
repository configuration after an attach until it reflects the project.

## Decision

The design for projects, roles, membership, repository attach and share, and environments
follows [07-webhooks-retention-projects](../07-webhooks-retention-projects.md): key
constraints (`^[a-z][a-z0-9-]{1,31}$`, immutable), `admin_privileges`, `soft_limit`,
`storage_quota_bytes` and `storage_quota_email_notification`, the predefined roles,
custom roles with the project action vocabulary, and the `<project_key>-` prefix rule for
repositories created inside a project. Attaching a repository updates its configuration
synchronously so a following read shows `projectKey`. Each project gets its
`<project_key>-build-info` repository. Storage usage is accounted per project and the
quota warns and blocks at the documented thresholds.

Global and project environments live in the Access API with the documented endpoints;
a repository's `environments` field follows Artifactory's project rule.

- **Outside a project** (no `projectKey`), `environments` is ignored, and none are stored.
  Sources:
  - JFrog's repository configuration spec marks the field "Mandatory when assigning
    repository to a project".
  - terraform-provider-artifactory's guide says it "is ignored by Artifactory, if the
    repository is not assigned to an existing project".
- **In a project:**
  - An empty list becomes `["DEV"]`, Artifactory's default on assignment. `repo.Validate`
    therefore no longer requires the list when `projectKey` is set.
  - Every entry must be DEV, PROD, a global environment, or one of the project's own
    environments. Project environments are named `<projectKey>-<name>`: the key, not the
    project's name, with the whole name at most 32 characters
    (terraform-provider-project, `resource_project_environment.go`).
- **Case and spelling:** names compare without regard to case and are stored in the
  environment's own spelling, so rename and delete find every repository that uses one.
- **Where it applies:** create, update, the batch endpoints and the descriptor, all through
  `repos.CheckEnvironments`.
- **Unknown names:** an unknown entry answers 400 and writes nothing. The message,
  "Environment 'X' does not exist", is our choice; no source documents Artifactory's.

**Global roles and environments** (`/access/api/v1/roles`, `/access/api/v1/environments`,
issue #77, `internal/access/core/roles`):
- **Built-ins:** their defaults are defined in code (`model.BuiltinGlobalRoles`
  in `internal/access/core/model`, `api.BuiltinEnvironments` in `internal/access/api`).
  - The roles are "Project Admin" (`ADMIN`, the whole global vocabulary on DEV and PROD)
    and Developer, Contributor, Viewer, Release Manager and Security Manager
    (`PREDEFINED`). Application Admin and ML roles are not implemented. P25 owns
    application roles and P32 owns ML roles, including their action vocabulary and
    authorization behavior, using P11's shared project-role foundation
    ([capability roadmap](../../implementation/capability-roadmap.md)).
  - **Editable predefined roles** (issue #100), as JFrog documents
    (`manage-global-roles`, `assign-environments-to-global-and-project-roles`):
    - PUT may change a `PREDEFINED` role's actions and environments. The change is
      stored as a role row of the same name and laid over the defaults; the built-in
      description stays.
    - Renaming, deleting or changing the type of any built-in role answers 400, as does
      any change to Project Admin.
  - **Default action sets:**
    - Developer's default is JFrog's one published example (`manage-project-roles`):
      Read and Write in DEV and PROD, expanded through the Basic actions' documented
      bundles.
    - JFrog documents the other predefined roles' defaults only in its UI, so those are
      chosen to match each role's documented purpose.
  - DEV and PROD can't be renamed or deleted.
- **Create-role response:** `POST /access/api/v1/roles` answers 201 with
  `{"code":"OK","message":"Role '<name>' created"}`, as JFrog's spec documents
  (`createglobalrole.json`). Our spec had drifted to "no body" and is corrected.
- **Names:** environment names are compared without regard to case, across global and
  project environments.
- **Rename** updates roles, project assignments and repository configurations in one
  transaction.
- **Delete** refuses, with 400, DEV, PROD, and any environment a repository or global role
  still names.
  - **To verify against a live Artifactory:** JFrog's spec lists only 204, 401, 403 and
    404 for this operation, so both 400 cases are artifactweave's choices: refusing
    built-ins, and refusing an environment still in use rather than detaching it.
- **Custom global roles:**
  - At most 30, as JFrog allows. Stored changes to predefined roles don't count.
  - Actions are checked against the 25-value global vocabulary, and environments must
    exist.
  - Deleting a role removes it from every project membership; a membership left with no
    role is removed.
- **Reading a role for the permission evaluator:** `roles.Service.Role`, then
  `model.RoleActions`.

## Rationale

Projects are how multi-team Artifactory instances are organised and what the Terraform
modules in those organisations already declare; without them the `jfrog/project`
provider is unusable and the `projectKey` fields on repositories mean nothing.

**Alternatives considered:**
- *Flat tenancy with naming conventions only*: no quotas, no project roles, provider
  incompatible.

## Storage accounting boundary (PRJ-04)

The [project creation guide](https://docs.jfrog.com/projects/docs/create-a-project)
defines quota over the total virtual size of assigned repositories and documents
the 75% warning and deployment refusal. Accounting uses owned logical content,
before CAS deduplication: live item rows, separately retained raw remote bodies
and virtual-owned transformed copies. Item-backed cache is counted through its
item; a standard share adds no receiving-project charge. Archive quotas remain P16.
The precise treatment of these internal representations is an **[I]** local
accounting decision, not a vendor runtime observation.

**[I]** a remote read whose optional cache publication cannot fit a hard quota
uses the existing validated transient path. Its attempted metadata publication
rolls back, leaving usage and any previous retained copy unchanged; each caller
owns a separate transient body and repeats current authority checks. The first
validated admission attempt can require a second upstream exchange for the
owned response, as with an initially unknown no-store response. This does not
bypass cache-fill permissions, integrity checks or unrelated storage failures.
Successful reads do not mark a healthy upstream as failed because local cache
capacity was refused. Soft/unlimited projects retain ordinary cache behavior.

Actual deltas settle for the complete metadata transaction before event/lease
publication fences. This admits net-zero moves above a lowered quota and rolls
back refused ownership transfers with their counters. Quota/email edits settle
zero bytes, so warning enablement and threshold rearming do not await another
upload. Durable warnings retain a project incarnation and occurrence identity;
SMTP delivery cannot promise exactly once after acknowledgement loss.

Refusals use the existing route envelope with HTTP 403 and the generic message
`project storage quota exceeded`, without owner names, usage totals or an
authentication challenge. Status and wording are **[I]** choices: primary quota
documentation establishes refusal but does not establish that wire answer.
The [mail-server guide](https://docs.jfrog.com/administration/docs/mail-server-configuration)
identifies project quota notifications as a mail-server use case. PRJ-04 supplies
its narrow configuration/delivery path; full descriptor administration remains P12.

The worker resolves current enabled project administrators, including current
group membership, under the project administration guard. It repeats the
project lifetime, warning nonce, current notification condition, recipient,
mail configuration and work-lease checks before SMTP DATA, with no metadata
transaction held during SMTP. Removed recipients and deleted/recreated projects
cannot reuse a queued warning. The remaining external race after that final
check cannot be made atomic with SMTP.

**[I]** delivery choices: missing/disabled mail transport follows the existing
queue retry/parking policy; obsolete warnings and occurrences without a current
deliverable project administrator are suppressed. An absent sender uses
`artifactweave@localhost`. Message wording reports the immutable transition's
logical bytes and quota, with a stable Message-ID and enqueue-time Date. TLS
certificates are verified; SMTP PLAIN authentication follows the standard
library's requirement for encryption except on localhost. These are local
delivery choices, not captured vendor notification text or SMTP observations.

## Constraints

- Project-scoped tokens and roles are evaluated by the permission model
  ([0006](0006-authorization-native-permission-model.md)).
- Project admins may create and delete repositories only within their granular privileges.

## Project-scoped remote source boundary (#614)

**Phase allocation (2026-11-19):** P11 owns documented project lifecycle, current
role authorization, environments, attachment, standard sharing and quotas. The
observation-dependent project-aware connection feature #487 belongs to P19a after
P19, with its full native/revocation matrix. Merged private authority and role services
remain credited P11 foundations; standard readOnly sharing is not the missing mode.

The [project remote source design](../../implementation/project-remote-sources.md)
defines the P9 source extension and P19a completion cases for
[#487](https://github.com/weaveplatform/artifactweave/issues/487). HTTP and same-platform
project sources have distinct identities; an internal connection carries the initiating
subject and receiving project instead of an administrator's loopback credential. Source
incarnations and generations fence cache reuse and publication, while current share and
caller permissions gate every response, including explicit cache reads after unsharing.

The [2026-11-19 audit](../project-scoped-remotes-contract-audit.md) supersedes the older
release and discovery-schema gaps. It distinguishes supported all-project sharing from
the restricted All Projects connection context. The sharing-mode mutation wire contract
remains unresolved; a read-only permission ceiling is not that mode. The design retains
**[I]** validation, cache and race decisions and makes no live 7.161.32 observation. P19a owns
sharing-mode administration, `projectScopedReadUpstream`, `upstreamRepoKey`,
`upstreamProjectKey`, `connectableRepos`, `projectScopedConsumers`, authorization and
native acceptance. Durable project/policy/share records are internal foundations, not
evidence that those administration and retrieval workflows are implemented.
P9 keeps the current URL requirement and parity exceptions; this design does not close
#487 or change this decision's Proposed status.

## Verification

- Key validation and prefix tests.
- Attach, share and unshare tests including the synchronous `projectKey` read.
- `jfrog/project` provider resources in the Terraform acceptance fixture.

## References

- [07-webhooks-retention-projects](../07-webhooks-retention-projects.md), projects section
- <https://docs.jfrog.com/projects/reference/llms.txt>
