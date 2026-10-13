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
