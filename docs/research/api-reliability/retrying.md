# Retrying generated-client requests

The generated Go clients issue one request per call. They do not automatically
retry arbitrary POST/PATCH actions. An error or timeout can occur after commit.

For a read, use a caller deadline, a small attempt budget, exponential backoff
with jitter, and `Retry-After` as a lower bound. Both integer seconds and HTTP-date
forms should be accepted. Do not retry a permanent validation/authentication error
or a 501 stub. Rate metadata is advisory; a shared limit can reject a request even
when the caller's own remaining count is positive.

## Keyed operations

An operation whose API description declares the shared `IdempotencyKey`
parameter takes an optional `IdempotencyKey` field in its generated params. Use
a quoted UUID, created once per intent outside the retry loop. Marshal the body
once and create a new reader over the same bytes for every attempt, and keep the
same credential and the same path and query. A changed body, or a changed
parameter that alters the effect, gets 422. A different key or credential is a
new intent and runs the effect again. Today these operations are all in the
Artifactory builds family:

| Operation | The key's resource | Also fingerprinted with the body | A replay answers |
| --- | --- | --- | --- |
| `appendBuild` | project, build name, number and optional started | nothing | 204 |
| `deleteBuilds` | build name and the numbers the query lists, or every run | `artifacts` | 200 with the runs the delete removed |
| `deleteBuildsMultiple` | nothing beyond the credential: the body names the runs | nothing | 200 with the runs the delete removed |
| `promoteBuild` | project, build name and number | nothing | 200 with the original promotion's messages |

A delete and a promotion keep what they reported (the runs deleted, the
promotion's messages) with their committed effect, so that a replay answers with
it although the runs are gone or the files have moved; the same request without
a key would now delete the number's next run or find nothing to move. A dry-run
promotion, and a fail-fast promotion that aborted, change nothing and keep no
receipt, so a retry runs them again.

The [compilable example and regression test](../../../pkg/client/artifactory/example_retry_test.go)
use the generated `AppendBuildWithBody` method, at most five attempts, a 90-second
overall deadline, a 35-second HTTP timeout and jittered exponential delays. They
close every response body and honor `Retry-After` without shortening it to fit a
deadline. The example refuses redirects and does not print a token or response
body. Supply authentication through `WithRequestEditorFn` using the same
credential on every attempt.

```go
key := strconv.Quote(uuid.NewString()) // once per new intent, outside the retry loop
payload := []byte(`[{"id":"compiled-module"}]`) // marshal once
params := artifactory.AppendBuildParams{IdempotencyKey: &key}
response, err := client.AppendBuildWithBody(ctx, "app", "42", &params,
    "application/json", bytes.NewReader(payload))
```

A promotion and a delete take their key the same way; the example file also
retries a promotion:

```go
promote := artifactory.PromoteBuildParams{IdempotencyKey: &key}
payload = []byte(`{"status":"released","targetRepo":"libs-release-local"}`)
response, err = client.PromoteBuildWithBody(ctx, "app", "42", &promote,
    "application/json", bytes.NewReader(payload))

numbers := "42"
remove := artifactory.DeleteBuildsParams{BuildNumbers: &numbers, IdempotencyKey: &key}
response, err = client.DeleteBuilds(ctx, "app", &remove)
```

Persist the key, exact bytes, credential reference and selected resource before
the first attempt if the client itself must recover after a restart. Never persist
plaintext credentials in an intent record merely for this purpose. Successful
initial execution and authorized replay return the same success status (204 for an
append, 200 for a delete or a promotion). Network failures and
429/500/502/503/504 can be retried with the original protected intent within its
retention window. Other responses require reconciliation or user action.

After 24 hours the receipt expires and the key can run the effect again. If the
client's retry budget expires, retain the unresolved intent; inspect the original
run and reconcile instead of inventing a new key. A deleted or renamed original
run (404 or 409), revoked permission or credential rotation changes recovery
conditions. The [decision](../decisions/0043-api-reliability.md) describes these
cases.

Publish, rename, item copy and move, CRAN archive moves and other unsafe
operations do not declare the key. In particular, an AQL POST may be a read
**or** a mutation. Sending a key to an operation that does not declare it returns
400 before its handler runs, with the message `Idempotency-Key is supported only
once, on an operation whose API description declares it`. Native
multipart/OCI/Terraform protocols have their own status/offset/checksum rules and
should use those reconciliation mechanisms.

## Access and tokens

No Access operation declares the key today. Its served writes converge on the
target the request names (a group, permission or user by name, a token by ID), so
a retry of the same request reaches the same state and needs no key. Token
issuance is `none` with `secret-response`: Access `createToken` and
`oidcTokenExchange`, and Artifactory's API key and legacy token routes. Their
answer is a new secret, so a replay would have to keep that secret to answer
again, and the replay store would become a store of live credentials. A key on
them answers the same 400. After an ambiguous token request, list the caller's
tokens, revoke any the client cannot account for, and request a new one.

## Adding a keyed operation

This part is for maintainers. Every keyed operation shares one replay
([`internal/platform/idempotency`](../../../internal/platform/idempotency/doc.go)),
as decision 0043's
[Replay for keyed operations](../decisions/0043-api-reliability.md#replay-for-keyed-operations)
sets out. Build append, delete and promotion use it; another operation takes three
steps.

1. **Declare the class.** Give the operation `x-idempotency: key` in its
   `api/src` source. On its own this changes nothing a client sees.
2. **Reference the parameter.** Add the shared `IdempotencyKey` parameter
   (`$ref: '#/components/parameters/IdempotencyKey'`) and run `make generate`.
   The operation table now marks the operation, the
   [key gate](../../../internal/platform/idempotency/gate.go) accepts one key on
   it, and the replay wraps its generated handler. Until the next step is done,
   `TestUnitKeyedOperationsHaveBindings` in `internal/app` fails, and a keyed
   request answers 500 rather than run unprotected. `TestAccAPIPrinciples` holds
   every served key operation to declaring the parameter and every 501 stub to
   declaring none.
3. **Adapt the handler and bind it.**
   - In the handler, call `idempotency.Report(ctx, id)` on success with the
     positive, stable ID of the resource the effect created or changed. A handler
     that reports nothing has its work rolled back and gets no receipt.
   - Make every store call with the context the handler receives, so the effect
     joins the replay's transaction. Write content-addressed blobs before the
     store calls, enqueue work inside the transaction, and move any call to
     another service behind the outbox or the work queue; an operation that must
     call out while the client waits stays `none` with `external-effect`.
   - Return an `idempotency.Binding` for the operation ID from the module's
     `KeyedBindings`, and register that function in `newKeyedOperations`
     ([`internal/app/keyed.go`](../../../internal/app/keyed.go)) under the
     spec's service name if the module has none yet. `Resource` makes the
     handler's first checks and returns the canonical resource (aliases
     resolved, timestamps at microsecond precision). `Fingerprint` returns the
     query parameters that change the effect without selecting its target, such
     as a dry-run flag. `Reload` locks and loads the resource by the receipt's
     ID, rechecks the current credential and permissions, and returns the
     success response built from the committed state. `Error` maps the
     handler's and reload's errors to the operation's answers, and `Name` names
     the effect in the 503 the budget gives.
   - Where the success body reports what the effect did rather than describing
     a resource, store that report in the effect's transaction and report its
     ID, as build delete and promotion do with their build reports
     (`BuildStore.PutReport`); the reload returns it unchanged.

The build bindings
([`idempotency.go`](../../../internal/artifacts/adapters/http/idempotency.go))
and their handlers ([`builds.go`](../../../internal/artifacts/adapters/http/builds.go))
are the worked examples. Give the operation a row in `keyedRows`
([`keyed_operations_test.go`](../../../test/server/artifacts/keyed_operations_test.go)),
which fails for a keyed operation without one, and test its keyed answers through
the router: first run, replay, changed body, deleted and moved resource, revoked
access and a duplicate in flight.
