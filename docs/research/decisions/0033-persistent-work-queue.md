# 0033: Persistent work queue

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

Status: Accepted

## Context

Package formats derive data from what is deployed:
- Maven metadata at the snapshot, artifact and plugin levels, and `maxUniqueSnapshots` (P7);
- Helm's `index.yaml` and NuGet's registration pages (P8);
- webhook deliveries, replication and cleanup (P11).

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
  after a rollback.

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
- Parked items need a fresh enqueue, or an operator action in a later phase, to run again.
- Development databases created before this table existed are recreated, since no release
  has been cut.

## Verification

- `pkg/store/storetest/work.go`: the contract on the in-memory, SQLite and PostgreSQL stores,
  including concurrent leasing, coalescing, fencing, crash recovery and transactions.
- `internal/sqlstore/sqlcommon/work_test.go`: driver-failure paths.
- `internal/service/work/work_test.go`: the worker pool, retries, parking, panics,
  heartbeats, lost leases, shutdown, `EnsureFresh`, `WaitIdle` and the metrics.
