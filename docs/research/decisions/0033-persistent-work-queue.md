# 0033: Persistent work queue

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

Status: Accepted

## Context

Package formats derive data from what is deployed:
- Maven metadata at the snapshot, artifact and plugin levels, and `maxUniqueSnapshots` (P7);
- Helm's `index.yaml`, one per repository (decision [0039](0039-helm.md));
- NuGet's snapshot cleanup and extracted PDB symbols. Its registration pages are computed
  on read from the package index instead (decision [0038](0038-nuget.md));
- the planned webhook deliveries, replication and cleanup of decision 0011 and the
  related designs.

Doing that work inside the deploy's write transaction does not scale:
- **PostgreSQL:** concurrent deploys of one package contend on the same derived row.
- **SQLite:** it has one writer, so a longer transaction slows every other write.
- **Bulk publishes:** a build that deploys fifty modules would recalculate on every upload.

Artifactory recalculates artifact-level Maven metadata after commit, on a queue with eight
workers, for these reasons. The eight workers come from the 6.20 source (research 27);
no 7.x document confirms that number, so it is **[I]** for 7.x. 7.10.1 and 6.23.0 changed
the plugin level: Artifactory calculates it only when the file deployed or deleted is a Maven
plugin ([RTFACT-20623](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-20623): "we need
to do so only if the file deployed/deleted is a maven plugin file"). The legacy 7.10.1 note,
"Maven plugin metadata is now calculated for every deploy or delete actions"
([legacy notes](https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf)),
reads the other way and is the weaker source.

Until now the only background mechanism has been the `leases` table: one singleton lease
per job, which keeps garbage collection and mirror repair from overlapping across nodes. It
cannot hold individual items of work.

## Decision

**Derived work runs asynchronously from a persistent work queue.** Its items live in the
metadata database, so work that is asked for survives a restart or a crash, and any node
may run it.

**Items.**
- **One item per kind and key.** A kind names a sort of work and is a code constant, such
  as a Maven package's metadata; the key names what it applies to, such as `g:a` in a
  repository.
- **Coalescing.** Work asked for again while an item is pending coalesces into it: the item
  keeps its schedule and takes the newest payload.
- **The payload** is a small JSON document of at most 64 KiB; the work reads current state,
  not data carried in the queue.
- **Enqueue within the change.** `Enqueue` runs inside the transaction of the change that
  needs the work, so the work is asked for exactly when the change commits, and not at all
  after a rollback. Item transfers buffer format enqueue requests, including nested
  transfers, and flush them in `(kind, key)` order before the outer transaction commits.
  This prevents opposite-direction moves from locking repository work rows in opposite
  order; an enqueue failure still rolls back both the transferred items and their work.

**Delivery and order.**
- **At least once.** Handlers are idempotent: an item can run again, for example when a node
  stops after doing the work but before recording it.
- **Serial per key:** an item is run by one worker at a time. There is no order across keys.
- **No lost updates.** Work asked for while an item runs marks it (`dirty_since`), and the
  item runs once more when the current run ends. A new run covers anything asked for before
  it began, so the mark clears when an item is leased.

**Leases.**
- **Taking work.** A worker leases due items for a lease period (default two minutes) and
  extends the lease every half period while the run lasts.
- **Fencing.** Each lease increments the item's generation. A worker whose lease ran out
  cannot complete, fail or extend the run another worker took over; the store answers
  `ErrConflict`.
- **Nodes.** On PostgreSQL, nodes lease with `SELECT ... FOR UPDATE SKIP LOCKED`, so they
  share the work without waiting on each other. SQLite's single-writer transaction does the
  same for one node.
- **Crash recovery.** A leased item whose lease ran out is due again, so a crashed node's
  work is taken over.

**Failures.**
- **Retries.** A failed run is retried with exponential backoff, from one second, doubling,
  up to ten minutes.
- **Parking.** After `max_attempts` runs (default ten) the item is parked until the key is
  enqueued again, which gives it a fresh set of attempts. Work asked for during a failing
  run is retried rather than parked, since it may succeed.
- **Panics** in a handler count as failures.

**Shutdown.** A stopping node cancels its runs and hands their leases back without
counting the attempt, after the HTTP server has drained, so another node or the next start
takes them at once.

**Readers.** A reader that must see derived data up to date calls `EnsureFresh`:
- a pending item is run there and then, in the reader's context;
- a run in progress elsewhere is waited for;
- a parked item is reported (`ErrParked`).

So clients never read stale derived data, while the write path stays short.

**Periodic kinds** (#283). A kind whose work Artifactory does on a timer, such as Maven's
`maxUniqueSnapshots` cleanup every five minutes, is registered with an interval
(`HandleEvery`). npm repositories ignore `maxUniqueSnapshots` (decision
[0034](0034-npm.md), #518). Maven and NuGet snapshot cleanups are periodic kinds, with
separate configured intervals. Their items are enqueued like any other, so a mark survives a restart, but
the workers do not take them as they fall due: a timer on each node takes every due item of
the kind once per interval (`RunDue`), with the usual leases, retries and parking. An item
asked for again during its run waits for the next tick: when the pass meets it again, the
pass keeps it leased and goes on, and hands it back unrun when no other item is due. If the
pass handed it back at once, its new due time could tie, on a coarse clock, with that of an
item not yet run, and the lease order would hand it out again ahead of that item (#576).
`EnsureFresh` still runs a pending one at once, waits for a pass that holds one, as for a
run elsewhere, and `WaitIdle` does not wait for a pending one.

**Maintenance operations too.** A JFrog operation that 7.x schedules rather than runs
before it answers is queued the same way, keyed by its repository: the reindex operations
and npm's strict-layout conversion (decision [0034](0034-npm.md)). The KB describes the
reindex as "an asynchronous reindexing of a local repository" (KB 000005641).
- **One reindex kind for every format** (#516). The item service runs `reindex`, keyed by
  the repository, through the format of the repository's package type, so a format that
  rebuilds an index (`format.Reindexer`) needs no kind of its own.
- **Helm's partial reindex** is `reindex.path`, keyed by `<repository>/<path>`, so one
  path's request never takes another's place, as the payload of a coalesced request would.
  A format that rebuilds one path (`format.PathReindexer`) does so; any other rebuilds the
  whole repository.
- A repository that is gone, or whose format keeps no index, owes nothing.
- No reader waits for such an item, so a format names none of them in `Owed`. A
  synchronous operation, PyPI's reindex without `async=true`, waits for its own item as a
  reader does (`EnsureFresh`), and answers once the run is done or as it failed.
- A conversion that leaves tarballs it cannot move fails, naming them, so the queue's
  log, retries and parked state carry its report.
- The operation's resume enqueues the parked item again, which resets its attempts.

Each reindex operation answers 501 until the format of its package type rebuilds an index,
and then as 7.x does (checked against 7.161.32 and SaaS 7.174.0 on 2026-10-19):

| Operation | Caller needs | Repositories | Runs | Answer | Basis |
|---|---|---|---|---|---|
| `POST /api/npm/<repo>/reindex` | Manage | local | queued | `Recalculating index for npm repository <repo> scheduled to run` | [D] archived [*Calculate npm repository metadata*](https://web.archive.org/web/20260210113712/https://jfrog.com/help/r/jfrog-rest-apis/calculate-npm-repository-metadata); decision 0034 |
| `POST /api/pypi/<repo>/reindex` | Manage | local; federated [I] | before the answer, unless `async` is `true` or `1` | `Recalculating index for pypi repository <repo> scheduled to run`, after a synchronous run too [I] | [D] [calculatepypirepositorymetadata](https://docs.jfrog.com/artifactory/reference/calculatepypirepositorymetadata.md); archived [*Calculate PyPI repository metadata*](http://web.archive.org/web/20251009074336/https://jfrog.com/help/r/jfrog-rest-apis/calculate-pypi-repository-metadata) |
| `POST /api/nuget/<repo>/reindex` | Manage | local; federated [I]. Remote, a remote's cache and virtual: 501 until P9 and P10 | queued | `NuGet reindex calculation for repository '<repo>' accepted.` | [D] [calculatenugetrepositorymetadata](https://docs.jfrog.com/artifactory/reference/calculatenugetrepositorymetadata.md); archived [*Calculate NuGet repository metadata*](http://web.archive.org/web/20260308210640/https://jfrog.com/help/r/jfrog-rest-apis/calculate-nuget-repository-metadata) |
| `POST /api/helm/<repo>/reindex` | Manage | local, federated | queued | `Reindex of helm repo <repo> was scheduled to run.` | [D] [calculatehelmchartindex](https://docs.jfrog.com/artifactory/reference/calculatehelmchartindex.md); archived [*Calculate Helm chart index*](http://web.archive.org/web/20260207042251/https://jfrog.com/help/r/jfrog-rest-apis/calculate-helm-chart-index), whose sample shows only `OK`; [S] the answer as the [helm-push-artifactory plugin](https://github.com/belitre/helm-push-artifactory-plugin) prints it (2018) [I] |
| `POST /api/helm/<repo>/<path>/reindex` | as Helm's [I] | as Helm's [I] | queued, for the path [I] | as Helm's [I] | [D] [helmchartspartialreindexing](https://docs.jfrog.com/artifactory/reference/helmchartspartialreindexing.md); [kubernetes-helm-chart-repositories](https://docs.jfrog.com/artifactory/docs/kubernetes-helm-chart-repositories.md) (from 7.105.2) |
| `POST /api/terraform/<repo>/reindex` | an administrator | local; federated [I] | queued | `Reindexing of Terraform repository <repo> was scheduled to run.` | [D] [calculateterraformindex](https://docs.jfrog.com/artifactory/reference/calculateterraformindex.md); archived [*Calculate Terraform index*](http://web.archive.org/web/20251118200048/https://jfrog.com/help/r/jfrog-rest-apis/calculate-terraform-index); [terraform-opentofu-and-terraform-backend-repositories](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md) |

- Only Helm's page names federated repositories. A federated repository is a local one that
  federates, and [RTFACT-27144](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27144)
  says the REST reindex reaches one, so PyPI, NuGet and Terraform reindex them too [I]. npm's
  answers 400 for one, as [#293](https://github.com/weaveplatform/artifactweave/issues/293)
  left it.
- A missing repository answers 404, a caller without the right 401 or 403, and a
  repository of another package type or class 400. The order of these checks and the text
  of each refusal are inferred [I].
- The partial reindex's path may span several segments, which no chi pattern can match
  before a literal, so the server matches it ahead of routing. A path that cleans to the
  repository's root reindexes the whole repository [I].
- The reindex operations of the other package types answer 501, as no format here serves
  them.

**Not the queue's job.** Work the change itself depends on stays in the change. For
example, a unique snapshot's build number is assigned at upload from the package index,
because the upload's name depends on it.

**Configuration.** `work.workers` (default 4), `work.lease` (default 2m) and
`work.max_attempts` (default 10), with the usual flag > environment > file precedence.

**Metrics** (decision [0016](0016-observability-and-health.md)), through the OpenTelemetry
metric API:

| Metric | Labels |
|---|---|
| `artifactweave.work.items` | `kind`, `state` |
| `artifactweave.work.oldest_pending` (seconds) | `kind` |
| `artifactweave.work.runs` | `kind`, `outcome` (`completed`, `failed`, `parked`, `released`, `lost`) |

Kinds are code constants, so the labels stay bounded.

## Rationale

- **A table in the metadata database** rather than an external broker keeps
  artifactweave a single binary with one dependency, and lets `Enqueue` join the change's
  transaction: there is no window where the change is committed but the work is lost, or
  the reverse.
- **One row per kind and key** makes coalescing a primary-key upsert, which is what turns a
  fifty-module publish into one recalculation per package.
- **Generations** rather than holder names fence a lapsed worker even when the same node
  takes the item back.
- **Rebuilding on read** keeps the write path short without letting a reader see stale
  data, which a purely asynchronous design would.

## Constraints

- An item enqueued for a kind with no registered handler waits; only registered kinds are
  leased.
- Parked items currently need a fresh enqueue to run again. P38 owns operator
  inspection, retry and recovery actions; P35 owns coordination across processes.
- Development databases created before this table existed are recreated, since no release
  has been cut.

## Owned follow-up

P38 owns authenticated queue inspection, parked-work retry and recovery, with
auditability and failure-path tests. P35 owns distributed scheduling and failover.
Neither assignment changes the implemented single-process leases or read freshness
contract described above.

The [capability roadmap](../../implementation/capability-roadmap.md) records the
phase boundaries under decision [0042](0042-capability-scope-and-supersession.md).
These assignments describe planned work, not current runtime support.

## Verification

- `pkg/store/storetest/work.go`: the contract on the in-memory, SQLite and PostgreSQL stores,
  including concurrent leasing, coalescing, fencing, crash recovery and transactions.
- `internal/sqlstore/sqlcommon/work_test.go`: driver-failure paths.
- `internal/service/work/work_test.go`: the worker pool, retries, parking, panics,
  heartbeats, lost leases, shutdown, `EnsureFresh`, `WaitIdle` and the metrics;
  `periodic_test.go`: periodic kinds, their timer and `RunDue`.
- `internal/format/npm/work_test.go` and `internal/server/npm_operations_test.go`: npm's
  queued reindex and conversion, a conversion that fails and is parked, and its resume.
- `internal/service/item/work_test.go`: the reindex kinds, through any format, of a whole
  repository and of a path. `internal/server/reindex_test.go`: each P8 reindex operation's
  permission, classes, timing and answer through a test-only format, and its 501 without
  one; `status_reindex_test.go` and `matrix_reindex_test.go`: their rows.
