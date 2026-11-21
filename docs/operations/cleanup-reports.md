# Package cleanup reports

Package cleanup records selection decisions and acknowledged item effects durably.
The current administrator can read a run after its policy or artifacts are deleted.
Run history belongs to the project lifetime that admitted it: recreating a project
or reusing a policy key does not transfer its old reports to the new owner.

All paths below start with `/artifactory`. Use the integer trigger returned by
manual execution as the `triggerId` or `runId`, and supply its original policy key.

| Request | Answer |
| --- | --- |
| `GET /api/cleanup/policies/view` | Current policy configuration, its latest actual package run and next scheduled occurrence when enabled. |
| `GET /api/cleanup/policies/runs/view` | Authorized run history with filters, paging and stable sorting. |
| `GET /api/cleanup/policies/runs/{policyKey}/{triggerId}` | One exact run, including its dry-run flag, status and acknowledged deletion counters. |
| `GET /api/cleanup/policies/runs/summaryReports?policyKey=KEY&triggerId=ID` | Frozen run configuration and totals derived from saved item outcomes. |
| `GET /api/cleanup/policies/runs/downloadReport?policyKey=KEY&runId=ID` | A nonempty terminal run's CSV, with `Content-Type: application/octet-stream`. |

Global policy views require current platform administration authority. A project
administrator supplies `projectKey` explicitly. `projectKeyList` further narrows run
history; it cannot widen that project context. Authorization and project lifetime
checks happen before filtering, counting or paging. A current platform administrator
can inspect retained history whose original project no longer exists.

Run views accept `policyKey`, `itemTypes`, `status`, `from`, `to`,
`excludeBlankExecutions`, `sortBy`, `sortOrder`, `page` and `pageSize`. Time bounds are
epoch milliseconds: `from` is inclusive and `to` is exclusive. Paging starts at 1,
defaults to 25 rows and allows at most 500 rows. The numeric run ID breaks sort ties.
An empty result has `lastPage: 0`. The [API source](../../api/src/artifactory/paths-cleanup.yaml)
lists the supported sort fields and status enums. Archive and warming statuses do
not become completed package executions.

A dry run reports selected eligible items and sizes without claiming deletion.
An actual run reports acknowledged deletions and retains failed, skipped and pending
item outcomes. The summary's `processedItems` and size fields describe selected items
for a dry run and actual effects otherwise. `cleanedItems` and `cleanedSize` remain
zero for a dry run. Virtual bytes count each item reference; content bytes count
shared content once. These are cleanup accounting facts: trash retention and physical
blob garbage collection proceed independently.

CSV columns are `repository,path,size,sha256,outcome,reason`. Quoting follows standard
CSV escaping, including embedded commas, quotes and newlines. Rows describe saved
item facts, including native companion removals. The server reads at most 100 item
outcomes per page and refreshes current authority between pages. Cancellation,
permission revocation, inconsistent metadata or a client write failure ends the
download without recording a completed download. `reportDownloaded` is marked only
after a successful local response. Empty and still-running executions have no CSV
and return 404. A failed or stopped run can have a CSV when it has saved item outcomes.

The CSV columns and plain CSV representation are local implementation choices under
the authored download contract; they do not claim a captured vendor CSV layout.
The [JFrog API reference](https://docs.jfrog.com/administration/reference/downloadcleanuppolicyrunreport)
specifies a binary report response, while its
[cleanup guide](https://docs.jfrog.com/administration/docs/cleanup-policies)
also discusses ZIP packaging. No observation-dependent report path fields are added.

The required [Terraform scenario](../../deploy/artifactweave/terraform/package-cleanup/README.md)
uses Terraform >=1.17 and Artifactory provider 12.11.14. It proves create, separate
enablement, update, empty plans and destroy alongside real Maven dry/actual runs,
summary/CSV checks and retained history after deletion. Provider state alone does
not qualify cleanup effects.
