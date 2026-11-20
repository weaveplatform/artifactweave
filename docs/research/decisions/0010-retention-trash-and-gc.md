# 0010: Retention, trash can and garbage collection

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378))

## Context

Artifactory's retention primitive for GitOps users is the package cleanup policy: a cron
schedule, search criteria (package types, repositories, included and excluded packages,
age, last download, keep-last-N versions, properties) and dry runs with reports. Deleted
items go to a trash can with a retention period, and a separate garbage collection
reclaims storage. Cron expressions use Quartz syntax (six or seven fields, with `?`, `L`,
`W`, `#` and an optional year).

## Decision

The trash can and garbage collection below are implemented. Package cleanup policies and
their Quartz scheduler remain P11. The additional path-pattern wire field whose
contract is unsettled is deferred to P19 observations and P19a implementation; its
absence does not defer documented package-policy execution. Build/bundle policies
still belong to P24; cold-tier archive policies remain P16. P24 owns
release-bundle lifecycle cleanup, P35 owns multi-node scheduling, and P38 owns background
task inspection and pruning controls. These are separate exits in the
[capability roadmap](../../implementation/capability-roadmap.md); the existing persistent
work queue and GC lease do not imply those features are complete.

**Cleanup policies** follow the Appendix in
[07-webhooks-retention-projects](../07-webhooks-retention-projects.md): the JSON shape,
the rule that a policy is created disabled and enabled through `/enablement`, dry runs,
run reports and the run status enumeration. Version-based criteria (`keepLastNVersions`,
`includedPackageVersions`) are evaluated over the package index
([0018](0018-package-index.md)) with the package type's comparator, never by path globbing.

**Candidate selection (P11 CLN-03).** Retention freezes a detached policy and
clock, then reads bounded package-index projections. Repository type, canonical
project, environment and exclusions narrow the query before paging. Package
cleanup excludes remote caches and virtual repositories, as the
[current cleanup guide](https://docs.jfrog.com/administration/docs/cleanup-policies)
requires. That guide supports federated repositories too and requires checking
downloads across their members. Federated policy configuration and availability
remain accepted. P11 selection evaluates local repositories and federated
repositories with an empty membership list: decision 0022 permits omitting the
self entry, so this subset needs no external-member download evidence. A bounded
membership-presence projection filters before paging and refreshes parent
identity; adding a member between pages refuses further local-only evaluation.
Federated repositories with configured members need peer-aware selection in P13
([#1798](https://github.com/weaveplatform/artifactweave/issues/1798)), whose peer
identity/evidence contracts are required before local statistics can safely
represent a federation. This is an explicit implementation gap for configured
members, not a vendor limitation. The generic index reader can describe remote
and federated indexes for other owners; retention requests the admitted hosted
classes with no federation members and refuses stale/foreign parents.

Keep-last-N protects the comparator-newest versions before inclusion filters;
zero still protects the newest. Version strings break comparator ties and supply
stable traversal keys. Age uses the supplied evaluation clock, complete 24-hour
days and 30-day legacy months. A never-downloaded lead uses its creation time;
missing creation time cannot become an invented old date. The last-download
predicate also preserves the existing version-level aggregate when a linked
archive was downloaded more recently than its descriptor; a change to that
statistic during evaluation invalidates the candidate page. This project choice
keeps active package content when a client's descriptor is already cached.
Matching excluded
properties wins over inclusion; all included property keys must match, with any
listed value sufficient for one key.

The owning format identifies lead files from existing index identity/metadata.
Selection reads no blob; file traversal retains one bounded page and lead
evidence rather than collecting the version's items. A classifier may read the
one existing opaque version-metadata document. Maven
unique snapshots use the current POM build order rather than checksum identity;
npm uses its canonical scoped tarball; Debian preserves admitted architectures;
Docker/OCI uses the canonical manifest preference; Go uses its module descriptor;
Helm and NuGet use the archive named by existing version metadata. The remaining
indexed formats reuse their canonical archive/descriptor validators, excluding
generated metadata, signatures and staged transactions. Ivy/SBT and custom Maven
descriptors follow the current compiled repository layout, including overrides;
an arbitrary XML file is never substituted. Hugging Face recognizes its documented
model/dataset metadata names; a snapshot without that lead is retained for
age/property evaluation rather than using an arbitrary model file.

A missing documented lead yields `missing_lead`; an embedding without a format
classifier explicitly yields `lead_contract_unavailable`. Neither invents old
timestamps or a substitute file. Existing formats without a populated package
index (including Generic, Cargo and Composer) retain the P28 ownership in
[0018](0018-package-index.md); cleanup builds no second index and performs no
item-tree/blob scan to fabricate package identities. For several leads in one version,
all must satisfy the rule; this conservative project choice preserves the whole
version rather than selecting an arbitrary architecture.

Each file snapshot carries actual ID/path/checksums/size, content and property
timestamps, download time and requested properties. Selection confirms lead
evidence and current comparator protection after its bounded candidate read;
changed lead evidence produces no candidates. These facts and internal reasons
are descriptive, not vendor report fields or deletion permissions. CLN-04 must
recheck current authority, item facts, keep protection and run/lease admission
before effects; CLN-05 owns report serialization. Selection itself changes no
items, bytes, usage, work or schedules.

**Scheduler (P11 CLN-01).** `internal/platform/schedule` supplies the shared Quartz
evaluator and durable logical positions for cleanup and single replication. It accepts
the documented six/seven-field grammar, named ranges and day modifiers, with the
[Quartz 2.3 year bound](https://www.quartz-scheduler.org/documentation/quartz-2.3.0/tutorials/crontrigger.html).
The bounded evaluator searches real calendar dates: a leap-day or impossible February
date cannot silently normalize into March. The originally proposed parser candidates
were checked before implementation; `go-quartz@v0.15.2` failed those two calendar
controls, and `influxdata/cron` lacks the required day modifiers. No parser dependency
was added.

Schedule position and a distinct occurrence's work enqueue share one metadata
transaction. Fresh revisions fence edits, disablement and recreation; worker admission
rechecks revision, enabled state and queue lease generation. The existing work queue
retains its at-least-once execution, retry and shutdown contract. Completed queue rows
can disappear without losing the logical position. Lock order is owner/domain state,
schedule, then queue; business/network work runs outside admission transactions.

Project scheduling policy is explicit: UTC by default, optional named IANA timezones,
skip nonexistent civil times and use the first instant of repeated civil times. Missed
fires catch up from the persisted logical fire in bounded passes. These are project
choices, not claimed vendor runtime observations. Cleanup/replication execution tickets
consume this foundation and own their run/report APIs. Existing fixed-interval trash,
GC and format maintenance jobs retain their current behavior.

**Trash can.** Deleting an item, or a repository with its content, moves the items to the
trash with the original repository and path; `trashcanConfig.retentionPeriodDays` governs
purge; `empty`, `clean` and `restore` follow the documented endpoints and permissions.

**Garbage collection.** `POST /api/system/storage/gc` removes blobs with no referencing
item or trash entry and refreshes the storage summary.

**Implementation (P3).**
- **Trash can.**
  - Unset, the trash can is on, with Artifactory's 14-day retention.
  - A trashed file keeps its blob reference, so it stays restorable. Purging an entry, by `clean`, `empty` or expiry, releases that reference.
  - Deleting a repository moves its content to the trash can under the repository's key, as deleting its root does: every item at its original path, with its own properties, as deleted by the administrator ([#504](https://github.com/weaveplatform/artifactweave/issues/504)).
    - It happens in the transaction that removes the repository, however large, so a failure leaves the repository, its content and the trash can as they were. The items are read a page at a time.
    - Each file's entry takes over the file's blob reference, so nothing is released while it is in the trash can and nothing is released twice once it leaves.
    - Another writer can come between reading the content and deleting it on PostgreSQL's read committed. A file it added meanwhile is deleted without an entry and its reference released. A file it removed meanwhile would leave an entry holding a reference no one removed, so the delete fails with nothing changed and a retry sees the content as it now is.
    - A remote's cache is released, as the trash can holds what was deleted from local repositories, and so is everything while the trash can is off.
    - The root's own properties go with the repository and are not restored.
  - Restoring the deleted repository's root, or a path below it, brings the items back into a local or federated repository that exists, at their original paths or below the path `to` names. A restore does not create the repository again.
  - `restore` takes the newest trashed copy of each path. Without a path in `to`, items return to their original paths; with one, the restored path lands there. A file at the destination is replaced, as a deploy replaces one. Every restored item carries `trash.originalRepository` and `trash.restoredTime` beside its own properties ([Artifactory 7.x check](#artifactory-7x-check)).
  - The trash can answers the storage API under the `auto-trashcan` key with the views any repository serves: the item info, `?list` with all its parameters, `?properties`, `?stats` and `?lastModified`. `?permissions` answers 400 to an administrator. An administrator sees all of it. Anyone else sees only what was trashed from repositories it holds deploy or manage on, judged at each item's original path, so exclude patterns hold, in every view.
  - A caller holding deploy or manage on no repository is refused. A path where the caller sees nothing is refused before the trash is read, and a listing leaves out what the caller does not see. Anonymous is refused even when it holds deploy, and only an admin sees what was trashed from a repository that no longer exists.
  - A refusal and a miss answer as in any local repository, under the `auto-trashcan` key, as 7.x keeps the trash can in one ([#1600](https://github.com/weaveplatform/artifactweave/issues/1600)). The item info and `?stats` refuse with 403 "Request for 'auto-trashcan:<path>' is forbidden for user: '<user>'.", anonymous included. `?properties` answers 404 "No properties could be found.". `?list`, `?lastModified` and `?permissions` refuse with 403 "Forbidden", or 401 "Unauthorized" with the challenge to anonymous, and `?list` refuses anonymous first with 403 "This resource is available to authenticated users only.". A path holding nothing the caller sees answers 404 "Unable to find item", `?stats` "Unable to find item path", `?lastModified` "Could not find item: auto-trashcan:<path>" and `?permissions` "Item auto-trashcan:<path> does not exist".
  - A trashed item carries its own properties and `trash.time`, `trash.deletedBy`, `trash.originalRepository`, `trash.originalRepositoryType` and `trash.originalPath`. `trash.time` is in Unix milliseconds. The repository's package type is recorded when the item is deleted, so it is still shown once the repository is gone.
  - `restore` needs Manage on the destination and delete or manage on the repository the items were deleted from, as `clean` does. The origin is checked at the requested path and at every restored item's original path, so an exclude pattern is honoured. One item not covered refuses the whole restore before anything moves.
  - The check at the requested path runs before the trash is read, so a refusal does not reveal whether anything is trashed there. Only an admin restores from a repository that no longer exists.
  - Permission failures on `clean`, `empty` and `restore` answer 400, as Artifactory reports them.
  - While the trash can is off, a delete removes the item for good, and every `auto-trashcan` storage view, its root included, answers 404 "Could not find resource" to every caller, an administrator too, as a path that was not there did when this was decided (#490). The check comes before the caller's rights, as a repository that does not exist answers 404 before them. Without credentials while anonymous access is off, the caller still gets 401.
  - What the trash can held when it was turned off stays. `restore`, `clean` and `empty` still act on it, and nothing in it expires until the trash can is on again, when the retention period applies to it as before.
- **Garbage collection** runs every `gc.interval` (4 hours) and on `POST /api/system/storage/gc`, under the `storage-gc` lease. A pass:
  - purges expired trash entries while the trash can is on;
  - reaps upload sessions untouched for 24 hours;
  - finishes removals a crashed pass left deleting;
  - deletes blobs unreferenced for `gc.grace_period` (an hour);
  - deletes content the index has no live row for, once the walk has seen it unindexed for the grace period across passes. Blob stores keep no times;
  - prunes tombstones older than a day.
- **Deleting a blob** takes three steps, for an indexed blob and an orphan alike (#64):
  1. Its index row is marked `deleting`, in a transaction that refuses while the blob is referenced. Content the index never recorded gets a `deleting` row.
  2. Its content is deleted.
  3. The row becomes a `deleted` tombstone, recording when the content went.
- **A deploy racing a removal of the same bytes** is refused rather than left referencing missing content:
  - A deploy writes its content before referencing it, and a content-addressed store skips content that is already there. So the content the deploy relies on can be the copy the collector is removing.
  - The reference fails while the row is `deleting`. The deploy then finishes the removal itself, which is safe because a deleting row has no references and can gain none, and answers 503, which the JFrog CLI retries.
  - Reviving a tombstone keeps its deletion time. A deploy whose reference lands on such a row checks, inside its transaction, that the content exists; a miss rolls the whole deploy back and answers 503.
  - Rows never collected cost the common path nothing.
  - A checksum deploy treats either case as a miss, so the client uploads the content.
  - Tombstones are kept a day, far longer than the milliseconds between a deploy writing content and referencing it.
- **The cold tier** is not collected until archiving (P16) writes the references its collection would depend on.
- **The storage summary** is computed on every request. `calculate` acknowledges and does nothing else. The trash can is counted in an `auto-trashcan` row and in the totals, as the repository Artifactory keeps it in: a path trashed twice is one file, and the folders above a trashed item are folders.

## Rationale

Cleanup policies are what the `artifactory_package_cleanup_policy` resource manages and
the closest thing Artifactory has to retention as code; implementing them over a package
index makes the version rules correct across formats. A Quartz parser is required because
`robfig/cron` does not accept the documented syntax.

**Alternatives considered:**
- *Standard five-field cron*: rejects the schedules users already have.
- *Deleting straight to GC*: loses the trash can semantics and the restore endpoint that
  operators rely on.

## Artifactory 7.x check

The decision stands against 7.161.32. The audit of P1 to P7
([#378](https://github.com/weaveplatform/artifactweave/issues/378)) confirmed the trash
can's defaults (on, 14 days, local deletes only), the trash and GC endpoints' statuses and
the 400 for their permission failures.

Restoring needs permission on both repositories. The destination needs Manage, as the
[reference page](https://docs.jfrog.com/artifactory/reference/restoreitemfromtrashcan.md)
states. The repository the items were deleted from needs delete or manage: from 7.41.4 a
non-admin restores only items deleted from repositories it holds delete permission on, and
cannot restore any other repository's
([general settings](https://docs.jfrog.com/installation/docs/general-settings), "Restoring
Deleted Repositories";
[7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed),
RTDEV-58624). 7.x refuses a restore without it with "Needs delete permissions"
(**[J]** [RTFACT-29653](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29653), open,
affects 7.55.13). Manage on the origin is accepted as well because the reference page names
it, and a permission can grant Manage without delete. Two details are inferred:
- **[I]** RTFACT-29653 reports that 7.x wants delete on the whole origin repository, so a folder-level grant does not let its holder restore that folder. artifactweave checks the requested path and every restored item's original path, so a folder-level grant is enough and its exclude patterns still hold.
- **[I]** The general settings page suggests a holder of delete on the origin restores into it without Manage; the reference page names Manage. artifactweave requires Manage on the destination.

Listing the trash can follows the same page. From 7.41.4 a holder of deploy or manage on a
repository sees what was trashed from it, and only that, and a user holding neither on any
repository does not see the trash can: "The contents of the Trash Can are filtered based on
the repositories to which the user has permissions"
([general settings](https://docs.jfrog.com/installation/docs/general-settings), "Restoring
Deleted Repositories"). **[J]** [RTFACT-27280](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27280)
describes the change that
[RTFACT-13242](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-13242) (Done) made: "users
with deploy permission of repositories are now able to see the Trash Can items for the
repositories that they have permission to". artifactweave applies this rule to the
`auto-trashcan` storage path. Three details are inferred:
- **[I]** The page and both issues describe the UI's tree. artifactweave refuses a signed-in caller at a path where it sees nothing, as the storage API refuses an unreadable path, and leaves what it does not see out of folder listings.
- **[I]** artifactweave judges each trashed item at its original path, so a folder-level grant shows only that folder's items and exclude patterns hold. 7.x may judge the repository as a whole, as RTFACT-29653 reports for restore.
- **[I]** Anonymous is refused even when it holds deploy, and only an admin sees what was trashed from a repository that no longer exists, as only an admin cleans or restores it. The refusal is the view's, as for any repository (#1600). 7.161.26's code grants more: `AuthorizationServiceBase.isGrantedOnTrashcan` lets the trash can's root be read by `canAccessTrashcan` (admin, or deploy or manage on any repository) and a path below it by `canRestoreFromTrashcan` (deploy or manage at the item's original path), with no rule for anonymous, and a repository that no longer exists gets a permission scope with no repository type (`AuthorizationServiceImpl.createArtifactPermissionScope`), which a plain ANY grant matches. No JFrog page, KB article or Jira issue says that either is intended: the general settings page, RTFACT-13242 and RTFACT-27280 speak of signed-in users holding deploy or manage on a repository, and none of them, nor [RTFACT-30167](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30167) or the KB's [How to Restore a Deleted Repository](https://jfrog.com/help/r/how-to-restore-a-deleted-repository/how-to-restore-a-deleted-repository), names anonymous access to the trash can or who sees a deleted repository's trash. So the rule stands until a live 7.x shows otherwise (#216).

The trash can serves the storage API's views as a repository does
([#455](https://github.com/weaveplatform/artifactweave/issues/455)). JFrog documents the
file list on `auto-trashcan` with `deep`, `depth`, `listFolders`, `mdTimestamps` and
`includeRootPath` (**[KB]** [How to search artifacts under trash can using REST
API](https://jfrog.com/help/r/artifactory-how-to-search-artifacts-under-trash-can-using-rest-api)).
Three details are inferred:
- **[I]** The other views answer as on any repository: `?properties` with the item's own properties and the `trash.*` ones, as 6.20 keeps both on the item in the trash can, `?stats` with zero counts under the download URL, for a file as 6.20 trashes a copy, and for a folder as in any repository, and `?lastModified`. The filter above holds in each.
- **[I]** `?permissions` answers 400 to an administrator: no permission target can name the trash can (**[J]** [RTFACT-30167](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30167), open), so the view has nothing to describe.
- **[I]** A trashed item's properties change when it is trashed, which `mdTimestamps` reports, and a folder that holds only what was trashed below it has no times or properties.

A trashed item carries `trash.time`, `trash.deletedBy`, `trash.originalRepository`,
`trash.originalRepositoryType` and `trash.originalPath`
([#477](https://github.com/weaveplatform/artifactweave/issues/477)). `trash.time` is in Unix
milliseconds: **[J]** [RTFACT-29920](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29920)
(Done, affects 7.61.1) compares it with an epoch in milliseconds. The other values are 6.20's
(`TrashServiceImpl.buildDeletedItemProperties`):
- **[I]** `trash.originalRepositoryType` is the repository's package type as `RepoType.getType()` spells it, the type's enumeration name in lower case, which is `yum` for rpm.
- **[I]** `trash.deletedBy`, `trash.originalRepository` and `trash.originalPath` are who deleted the item, the key of its repository and its path there.

A restore sets `trash.originalRepository` and `trash.restoredTime` on every item it brings back, beside the item's own properties (**[J]** [RTFACT-29810](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29810), Done, affects 7.55.10: the expected and the observed properties of restored items both include them). Their values are inferred from 6.20 (`TrashService`, `TrashInterceptor.afterMove`):
- **[I]** `trash.originalRepository` is the key of the repository the item was deleted from, also when it is restored into another repository.
- **[I]** `trash.restoredTime` is the time of the restore in Unix milliseconds. A restore time recorded by an earlier restore gives way to the new one.

The storage summary follows 7.x
([#390](https://github.com/weaveplatform/artifactweave/issues/390)): an administrator or a
token scoped `system:info/storage:r` reads it
([getstoragesummaryinfo](https://docs.jfrog.com/artifactory/reference/getstoragesummaryinfo.md);
[7.117.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed)),
its rows carry `usedSpaceInBytes` and `projectKey` (**[S]** jfrog-client-go's live
`storageInfoTest` at `3b4b900d8174`), and the trash can has its own `auto-trashcan` row,
which the totals include, as three 7.x captures show
([research 15, section 2.2](../15-data-model.md#22-item-artifactory-artifacts-storage-api)).
`optimization` is `binariesSize` over `artifactsSize`, as JFrog's example and the captures
show, and an object store's `storageDirectory` is "Filesystem storage is not used", 6.20's
text for a chain with no local directory **[I]**
([#461](https://github.com/weaveplatform/artifactweave/issues/461)). A blob mirror reports
the directory of its first `fs` member, in the order `blob.mirror.members` lists them, and
that directory's volume space, or that text when it has no `fs` member. 6.20 reports the
first `file-system` or `cache-fs` provider of a chain and measures that directory
(`BinaryServiceImpl.getBinariesDir`, `FileStoreStorageSummary`), and no 7.x capture shows
a chain of several providers **[I]**
([#490](https://github.com/weaveplatform/artifactweave/issues/490)).

A repository delete keeps the repository's content in the trash can
([#504](https://github.com/weaveplatform/artifactweave/issues/504)). JFrog's general settings page refers to restoring deleted
repositories from the trash can ([general
settings](https://docs.jfrog.com/installation/docs/general-settings), "Restoring Deleted
Repositories"), and 7.x fixed "restoring a deleted repository root from the Trash Can"
copying the root's properties onto the restored root folder and everything below it
(RTDEV-84660, [7.146.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714615-self-managed) and [SaaS 7.155.3](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71553-saas)). A restore needs a destination
that exists: the trash can's restore window names a "Target Repository"
([trash can](https://docs.jfrog.com/artifactory/docs/trash-can)), the API requires `to`
and its example restores `npm-local` into `npm-local2`
([restoreitemfromtrashcan](https://docs.jfrog.com/artifactory/reference/restoreitemfromtrashcan.md)),
and **[KB]** [How to Restore a Deleted
Repository](https://jfrog.com/help/r/how-to-restore-a-deleted-repository/how-to-restore-a-deleted-repository)
creates "a new repository of the same type" first and restores into it. A restore into a
repository that does not exist answers 400 "Destination repository '<key>' does not exist".
Four details are inferred:
- **[I]** The root's own properties are not kept anywhere. RTDEV-84660 says only that a restore must not copy them onto what it brings back, and no source shows them on the deleted repository's folder in the trash can.
- **[I]** A restore into a local repository of another package type is accepted. The KB creates one of the same type, and no source says another type is refused.
- **[I]** A federated repository's content is trashed as a local repository's is, as a single delete from it is.
- **[I]** A repository deleted by `config apply` or the bootstrap descriptor, which run outside a request, is trashed with no `trash.deletedBy` value.

The trash can's storage views answer 404 while the trash can is off
([#490](https://github.com/weaveplatform/artifactweave/issues/490)). 7.x fixed "an issue
whereby Trash Can files were accessible via a native browser, even though the Trash Can
feature was disabled" ([SaaS 7.170.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71700-saas),
RTDEV-97807), and its UI shows the trash can only "when enabled"
([trash can](https://docs.jfrog.com/artifactory/docs/trash-can)). Nothing in it expires
while it is off: "The Small Garbage Collector won't work if the Trash Can is disabled", and
that collector is the one that removes trashed items past their retention period
(**[KB]** [The complete Garbage Collection Guide](https://jfrog.com/help/r/artifactory-artifactory-garbage-collection)).
6.20 agrees: `TrashServiceImpl.getGCCandidatesFromTrash` returns nothing while the trash can
is off, and `BinaryServiceImpl` then runs only the full collection. Three details are
inferred:
- **[I]** The status and message. 7.161.26 has no such check, which came with RTDEV-97807, and no source shows the answer, so the views answer 404 "Could not find resource", artifactweave's answer for a path that was not there when this was decided, to every caller who reaches them, as for a repository that does not exist. The storage API now answers a missing path in 7.x's words, "Unable to find item" and the like (#1600), and a download through the item routes its own miss, 7.x's "File not found." with the path (#1572); neither describes a trash can that is off.
- **[I]** `restore`, `clean` and `empty` still act on what the trash can holds while it is off. 6.20's `TrashcanResource` and `RestoreArtifactService` check no setting, and no 7.x source says otherwise.
- **[I]** Turned on again, the trash can shows what it held and applies the retention period to it, as 6.20 keeps the items in the trash repository throughout.

## Constraints

- Policies run in one process; a second process would double-run them.
- Cold-storage archive policies and smart archiving run on this engine and scheduler, but
  move content to the cold blob tier rather than the trash can
  ([0024](0024-cold-storage-tier.md)).

## Verification

- Cron parser conformance test over the documented examples.
- Policy evaluation tests per criterion over a fixture package index.
- Acceptance: a policy dry run and run with reports, trash restore, GC storage numbers.
- `artifactory_package_cleanup_policy` Terraform acceptance.

## References

- [07-webhooks-retention-projects](../07-webhooks-retention-projects.md), retention section
- <https://docs.jfrog.com/administration/reference/storage-and-archive/llms.txt>
- <https://github.com/reugn/go-quartz>
- <https://github.com/influxdata/cron>
