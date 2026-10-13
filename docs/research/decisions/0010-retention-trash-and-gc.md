# 0010: Retention, trash can and garbage collection

Status: Proposed

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
  - deletes blobs unreferenced for `gc.grace_period` (an hour);
  - deletes content the index has no row for, once the walk has seen it unindexed for the grace period across passes. Blob stores keep no times.
- **Deleting a blob** removes its index row before its content. A checksum deploy finds content only through the index, so once the row is gone the same content has to be uploaded again. The one case that can lose data is a full upload of identical content landing between the row delete and the content delete. The pass checks the index on both sides of the content delete, and reports and logs that case.
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

## Constraints

- Policies run in one process; a second process would double-run them.
- Cold-storage archive policies and smart archiving are out of scope and answer `501`.

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
