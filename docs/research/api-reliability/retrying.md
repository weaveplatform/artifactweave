# Retrying generated-client requests

The generated Go clients issue one request per call. They do not automatically
retry arbitrary POST/PATCH actions. An error or timeout can occur after commit.

For a read, use a caller deadline, a small attempt budget, exponential backoff
with jitter, and `Retry-After` as a lower bound. Both integer seconds and HTTP-date
forms should be accepted. Do not retry a permanent validation/authentication error
or a 501 stub. Rate metadata is advisory; a shared limit can reject a request even
when the caller's own remaining count is positive.

For build append, use the optional generated `AppendBuildParams.IdempotencyKey`
field with a quoted UUID. Marshal the body once and create a new reader over the
same bytes for every attempt. Keep the same credential, project, build name,
number and optional started timestamp. A changed body gets 422. A different key
or credential is a new intent and can append again.

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

Persist the key, exact bytes, credential reference and selected resource before
the first attempt if the client itself must recover after a restart. Never persist
plaintext credentials in an intent record merely for this purpose. Successful
initial execution and authorized replay both return 204. Network failures and
429/500/502/503/504 can be retried with the original protected intent within its
retention window. Other responses require reconciliation or user action.

After 24 hours the receipt expires and the key can perform another append. If the
client's retry budget expires, retain the unresolved intent; inspect the original
run and reconcile instead of inventing a new key. A deleted/renamed original run,
revoked permission or credential rotation changes recovery conditions. The
[decision](../decisions/0043-api-reliability.md) describes these cases.

Token issuance, refresh, publish/promote/move, CRAN archive moves and other unsafe
operations do not inherit append's guarantee. In particular, an AQL POST may be
a read **or** a mutation. Sending a key to an unsupported route returns 400 before
its handler runs. Native multipart/OCI/Terraform protocols have their own
status/offset/checksum rules and should use those reconciliation mechanisms.

## Adding a keyed operation

This part is for maintainers. Every keyed operation shares one replay
([`internal/platform/idempotency`](../../../internal/platform/idempotency/doc.go)),
as decision 0043's
[Replay for keyed operations](../decisions/0043-api-reliability.md#replay-for-keyed-operations)
sets out. Build append is the first; another operation takes three steps.

1. **Declare the class.** Give the operation `x-idempotency: key` in its
   `api/src` source. On its own this changes nothing a client sees.
2. **Reference the parameter.** Add the shared `IdempotencyKey` parameter
   (`$ref: '#/components/parameters/IdempotencyKey'`) and run `make generate`.
   The operation table now marks the operation, the
   [key gate](../../../internal/platform/idempotency/gate.go) accepts one key on
   it, and the replay wraps its generated handler. Until the next step is done,
   `TestUnitKeyedOperationsHaveBindings` in `internal/app` fails, and a keyed
   request answers 500 rather than run unprotected. The gate's refusal then names
   the rule instead of build append, which changes only answers to requests that
   send a key where none is accepted.
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

Build append's binding
([`idempotency.go`](../../../internal/artifacts/adapters/http/idempotency.go))
and its handler ([`builds.go`](../../../internal/artifacts/adapters/http/builds.go))
are the worked example. Test the operation's keyed answers through the router:
first run, replay, changed body, deleted and moved resource, revoked access and a
duplicate in flight.
