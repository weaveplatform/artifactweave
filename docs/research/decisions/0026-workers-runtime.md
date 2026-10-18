# 0026: Workers runtime

Checked against: Artifactory 7.161.32

Status: Proposed

## Context

JFrog Workers attach TypeScript functions to platform actions: before-actions that can
stop a request, after-actions that run asynchronously, HTTP-triggered workers
(`GENERIC_EVENT`) and, from 7.104.5, scheduled workers (`SCHEDULED_EVENT`) on a cron
expression ([research 16](../16-enterprise-products.md#5-workers),
[7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed)). The action list is the one
`api/src/worker/paths-workers.yaml` specifies; research 16 maps it to the current 7.x
event catalogue. The owner brought Workers into
scope on 2026-10-13 and chose goja, a pure-Go JavaScript engine, with esbuild transpiling
TypeScript in-process, so the single static binary stays a single static binary.

## Decision

**Compilation.** On create and update, the worker's `sourceCode` is transpiled by esbuild's
Go API from TypeScript to JavaScript in a target that goja executes (async functions
lowered to generators where goja requires it). `import` statements are rejected with 400,
as in JFrog. Sources above 100,000 characters are rejected. The compiled program is cached
by content hash; a compile error is returned on save, never at first execution.

**Execution.** Each execution runs in a fresh goja runtime (programs are shared, runtimes
are not), calls the default export with `(context, data)` and awaits its promise. The
`context` object provides what JFrog documents: `baseUrl`; `platformToken`, a short-lived
access token scoped to the worker's owner and the action; `clients.platformHttp`, an
in-process client to this instance's own API; `clients.axios`, an outbound HTTP client;
`wait(ms)`; `state`, persisted per worker with JFrog's default limits (10 items,
40-character keys, 250-character values; not saved when the execution fails);
`getRemainingExecutionTime()`; `crypto` with `hash`, `hmac` (sha256, sha512) and
`randomBytes` (at most 4,096 bytes). JFrog has since added
`platformHttp.request(method, body, headers)` (Workers 1.216) and fire-and-forget `axios`
methods (`getAndForget`, `postAndForget`, `deleteAndForget`; Workers 1.494 SaaS, 1.542
self-managed), which return as soon as the request is scheduled ([Workers releases](https://docs.jfrog.com/releases/docs/jfrog-workers-release-information));
artifactweave provides both. `console` output is captured into the execution
record. No filesystem, no timers other than `wait`, no Node modules.

**Execution hosts.** Workers do not run inside the registry process. A small pool of
helper processes, the same `artifactweave` binary started with a hidden `worker-host`
subcommand, each runs one execution at a time in a fresh goja runtime and talks to the
server over a pipe with length-prefixed JSON frames. One execution per helper is what
makes a memory cap enforceable with goja: the helper's heap growth during a run is that
run's allocation, so it can be measured exactly. A crash, a kill or a leak in a script
costs one helper, never the server. Helpers are started when the first worker is enabled,
replaced when they exit and recycled after a fixed number of executions to shed any slow
leak.

**Limits and defaults.** The defaults follow JFrog's documented Workers defaults so
existing workers behave the same; every value is configurable globally under `workers.`
in the application configuration.

| Limit | Default | Enforcement |
|---|---|---|
| Memory per execution | 64 MiB | A watchdog in the helper samples heap in use (`runtime/metrics`) every 10 ms against the baseline taken before the run and calls goja's `Interrupt` when growth exceeds the cap. The helper sets `debug.SetMemoryLimit` to its baseline plus twice the cap so the collector works hard before the ceiling. If heap reaches baseline plus twice the cap, or the interrupt has not landed within 100 ms, the server kills the helper and starts a new one. The execution fails with a memory-limit error either way |
| CPU time per execution | 1 s | Process CPU time of the helper (`getrusage` on Unix, `GetProcessTimes` on Windows), sampled by the same watchdog; exact because the helper runs nothing else |
| Wall-clock call time | 5 s per call | goja `Interrupt` from a timer; `context.getRemainingExecutionTime()` reports what is left |
| Execution time | 20 s overall | the ceiling for a whole execution including awaited platform calls |
| Call stack depth | 100 | goja `SetMaxCallStackSize` |
| Payload in and out | 100 KiB | checked before the payload is sent to the helper and on the result |
| Source size | 100,000 characters | checked at save time |
| Helpers | `min(4, GOMAXPROCS)` | executions queue for a free helper; a queue wait over 30 s (JFrog's `maxWaitingInQueueMillis` default) fails the execution |

At most one execution of a given worker runs at a time (JFrog does not support concurrent
runs of one worker's state). After ten failures within ten minutes a worker is blocked
with exponential back-off up to 17 minutes, reset daily, matching JFrog's documented
behaviour. Memory, CPU and time failures are recorded in the execution history with
distinct error codes.

**Outbound network.** `clients.axios` only reaches hosts on an allow-list in the
application configuration (empty by default), never link-local or loopback addresses,
and never this instance except through `platformHttp`. Self-managed JFrog Workers also
refuse some hosts: with `enableLocalNetworkValidation`, on by default, `axios` cannot call
localhost, "unicast IPs" or JFrog's domain names, and any other host is allowed
([Workers YAML](https://docs.jfrog.com/installation/docs/worker-yaml-configuration.md)).
The allow-list is the stricter of the two (DV-006).

**Hook points.** Before-actions run synchronously in the request path of the item, upload,
download, copy, move, delete, property, repository, build-info, token and replication
services; their result (`{status, message}`) decides whether the request proceeds, is
refused (403 with the message) or proceeds with a warning header. After-actions are
dispatched from the event bus that also feeds webhooks
([0011](0011-webhooks.md)), with retries. `ALT_REMOTE_PATH`, `ALT_REMOTE_CONTENT` and
`ALT_ALL_RESPONSES` hook the remote fetcher ([0007](0007-remote-repositories.md)).
`AFTER_WORKLOAD_STATE_CHANGE_REQUEST` has no artifactweave event and is accepted but never
fires. `GENERIC_EVENT` workers run from `POST /worker/api/v1/execute/{workerKey}`.

**Filters.** `filterCriteria.artifactFilterCriteria` (repository keys, include and exclude
patterns, `anyLocal`, `anyRemote`, `anyFederated`) is evaluated before a runtime is
created, so a worker that does not match costs nothing.

**Catalogue and tooling endpoints.** `/worker/api/v1/actions` and `/v2/actions` list the
actions artifactweave supports with its own sample payloads, sample code and TypeScript
type definitions; `scaffold/tsconfig` returns a configuration targeting ES2022. `test`
runs unsaved code against a supplied payload without side effects through
`platformHttp` (writes are refused); `rerun` replays a recorded execution's payload.
Execution history keeps the payload, result, logs, duration and status per execution,
pruned by age.

**Secrets.** Values a worker needs (for example an outbound API key) are stored as worker
secrets, sealed ([0017](0017-sealed-columns-and-master-key.md)), and exposed to the worker
on `context` *(the JFrog field name for secrets is unverified)*.

## Rationale

goja is mature, pure Go and used at scale for user scripts; esbuild's Go API transpiles
TypeScript without a Node toolchain. Together they keep the binary static: the helper
processes are the same binary, so no separate runtime is shipped or versioned. A
before-hook pays one pipe round trip to a warm helper, the price of the enforceable memory
cap and the crash isolation described under execution hosts. Evaluating filters before creating a runtime and creating a fresh
runtime per execution keep isolation simple: no state leaks between runs except through
`state`.

Alternatives considered (by the owner):

- *QuickJS on wazero.* Real memory limits and stronger isolation, at the cost of less
  mature bindings and slower start-up.
- *goja inside the server process with heap sampling.* No process management, but
  allocations cannot be attributed to one execution while others run, so the cap would be
  approximate and a runaway script would still pressure the server. Rejected in favour of
  helper processes.
- *A Node or Deno sidecar.* Most compatible with JFrog's runtime, but breaks the single
  binary.

## Constraints

- The memory cap is measured, not reserved: a script can overshoot by what it allocates
  in one 10 ms sampling interval before the interrupt, bounded by the hard kill at twice
  the cap. Platform calls made through the server count against the execution's time
  but not its memory.
- Each helper costs a Go runtime's baseline memory (a few MiB); helpers run only while
  at least one worker is enabled.
- goja implements ECMAScript with gaps; features esbuild cannot lower and goja does not
  support fail at save time with the compile error, and the supported subset is documented
  for authors.
- Before-actions add latency to the hooked requests; the execution time of before-actions
  is reported in metrics per worker.
- The stop and warn result statuses per action are not documented beyond
  `DOWNLOAD_PROCEED`; artifactweave defines `<VERB>_PROCEED`, `<VERB>_STOP` and
  `<VERB>_WARN` per action and records them as unverified until captured.

## Verification

- A TypeScript `BEFORE_DOWNLOAD` worker refuses a download and an `AFTER_CREATE` worker
  sets a property, in the acceptance suite.
- An infinite loop is interrupted at the timeout and recorded as failed; ten failures
  block the worker.
- A worker that allocates past 64 MiB fails with the memory-limit error, the server's own
  heap is unaffected, and the next execution runs on a healthy helper; a script that
  ignores the interrupt is killed and its helper replaced.
- A worker that burns more than 1 s of CPU fails with the CPU-limit error.
- Outbound requests to a host outside the allow-list and to loopback are refused.
- Terraform `platform_workers_service` applies, plans clean and destroys.

## References

- [Research 16](../16-enterprise-products.md), section 5
- [0007](0007-remote-repositories.md), [0011](0011-webhooks.md), [0017](0017-sealed-columns-and-master-key.md)
- goja: <https://github.com/dop251/goja>
- esbuild Go API: <https://pkg.go.dev/github.com/evanw/esbuild/pkg/api>
- <https://docs.jfrog.com/administration/docs/typescript-code-for-workers.md>
- <https://docs.jfrog.com/administration/docs/workers-limitations.md>
