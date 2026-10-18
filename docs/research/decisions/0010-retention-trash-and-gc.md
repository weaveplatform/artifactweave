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

**Cleanup policies** follow the Appendix in
[07-webhooks-retention-projects](../07-webhooks-retention-projects.md): the JSON shape,
the rule that a policy is created disabled and enabled through `/enablement`, dry runs,
run reports and the run status enumeration. Version-based criteria (`keepLastNVersions`,
`includedPackageVersions`) are evaluated over the package index
([0018](0018-package-index.md)) with the package type's comparator, never by path globbing.

**Scheduler.** `internal/scheduler` runs cleanup policies, replication, trash purge and
GC in process. Cron expressions are parsed by a Quartz-syntax parser; the candidates are
`github.com/reugn/go-quartz` and `github.com/influxdata/cron`, chosen by a conformance
test over every cron string in JFrog's documentation. Runs are recorded with a trigger id
so `/runs/view`, `/runs/{policyKey}/{triggerId}` and the CSV report work.

**Trash can.** Deleting an item moves it to the trash with the original repository and
path; `trashcanConfig.retentionPeriodDays` governs purge; `empty`, `clean` and `restore`
follow the documented endpoints and permissions.

**Garbage collection.** `POST /api/system/storage/gc` removes blobs with no referencing
item or trash entry and refreshes the storage summary.

**Implementation (P3).**
- **Trash can.**
  - Unset, the trash can is on, with Artifactory's 14-day retention.
  - A trashed file keeps its blob reference, so it stays restorable. Purging an entry, by `clean`, `empty` or expiry, releases that reference.
  - `restore` takes the newest trashed copy of each path. Without a path in `to`, items return to their original paths; with one, the restored path lands there. A file at the destination is replaced, as a deploy replaces one.
  - The trash can is listed through the storage API under the `auto-trashcan` key.
  - Permission failures on `clean`, `empty` and `restore` answer 400, as Artifactory reports them.
- **Garbage collection** runs every `gc.interval` (4 hours) and on `POST /api/system/storage/gc`, under the `storage-gc` lease. A pass:
  - purges expired trash entries;
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
- **The storage summary** is computed on every request. `calculate` acknowledges and does nothing else.

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
the 400 for their permission failures. It found these differences
([research 02](../02-artifactory-core-api-inventory.md#trash-can-and-garbage-collection)):
- 7.x differs: a non-admin restores only items deleted from repositories it holds delete permission on, and cannot see or restore any other repository's ([general settings](https://docs.jfrog.com/installation/docs/general-settings), "Restoring Deleted Repositories", from 7.41.4; [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed), RTDEV-58624); fixed by [#380](https://github.com/weaveplatform/artifactweave/issues/380).
- 7.x differs: a restored item also carries the properties `trash.originalRepository` and `trash.restoredTime` (**[J]** [RTFACT-29810](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29810), Done, affects 7.55.10); fixed by [#401](https://github.com/weaveplatform/artifactweave/issues/401).
- 7.x differs: storage summary rows carry `usedSpaceInBytes`, a number, and `projectKey` (**[S]** jfrog-client-go's live `storageInfoTest` at `3b4b900d8174`); fixed by [#390](https://github.com/weaveplatform/artifactweave/issues/390).
- 7.x differs: a non-admin whose token has the `system:info/storage:r` scope may read the storage summary ([getstoragesummaryinfo](https://docs.jfrog.com/artifactory/reference/getstoragesummaryinfo.md); [7.117.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed)); fixed by [#390](https://github.com/weaveplatform/artifactweave/issues/390).

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
