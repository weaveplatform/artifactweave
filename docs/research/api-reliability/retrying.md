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
