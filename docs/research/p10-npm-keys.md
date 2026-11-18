# P10 npm virtual signing keys

Research date: **2026-11-18**. Scope: [NPM-04 / #1319](https://github.com/weaveplatform/artifactweave/issues/1319), read live, and [decision 0034](decisions/0034-npm.md#signatures). **[D]** means current first-party documentation; **[S]** means inspected source at the identified revision; **[I]** means an implementation inference still requiring [#216](https://github.com/weaveplatform/artifactweave/issues/216). No trial or live Artifactory capture was used.

## Current vendor evidence

**[D]** JFrog's [npm guide](https://docs.jfrog.com/artifactory/docs/npm-repositories#npm-client-implications-for-npm-audit-signatures), read afresh, documents signing from **7.83.1**, upstream signatures through remotes, and missing-signature errors when virtual members mix signed and unsigned packages. Its known-issues section explicitly requires `.npm/keys.json` in restrictive include patterns. This establishes that an unsigned member must not suppress keys supplied by a signed member; it does not specify the union algorithm, empty response or failure statuses.

**[D]** The current [known-issues page, Artifactory 7.83](https://docs.jfrog.com/releases/docs/artifactory-known-issues#artifactory-783-known-issues) retains **RTDEV-42377**: mixed virtuals now produce missing-signature reporting; the earlier 404 caused clients to bypass that audit stage. Its fix-version cell is empty. The linked [Jira REST issue](https://jfrog.atlassian.net/rest/api/2/issue/RTDEV-42377) returned 404 during this check, so no independent Jira claim is made.

**[D]** Public release notes now reach [self-managed **7.173.21**, 17 November 2026](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-717321-self-managed) and [SaaS **7.178.1**, 12 November](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71781-saas). No later public signing-key aggregation contract was found. Self-managed detailed fixes now require authenticated access; this check covers public highlights only. The [7.146.7 provenance addition](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed) extends remote/virtual attestations, without specifying key merging. Targeted JFrog KB searches found no additional keys-endpoint contract.

## Exact native-client contract

**[S]** Inspected CLI versions are **11.20.0**, commit [`d12b9434dd01`](https://github.com/npm/cli/blob/d12b9434dd010b5fb7044c3cc149cdda317813f8/lib/utils/verify-signatures.js#L181), and **12.2.1**, commit [`ae98c62586b6`](https://github.com/npm/cli/blob/ae98c62586b6fea44114f72e249e246a2981de70/lib/utils/verify-signatures.js#L181). Both `setKeys` implementations, lines **181–220**, first query Sigstore TUF, then fall back to `/-/npm/v1/keys` only for a missing TUF target. They map each key's base64 `key` to PEM. Registry **400 and 404** errors become no keys; all other errors propagate. TUF initialization/network errors can prevent the registry request altogether, so native tests need reachable TUF infrastructure.

**[S]** Both files, lines **332–358**, count unsigned versions as missing when the registry supplies a nonempty key list; lines **53–60** make missing/invalid signatures exit unsuccessfully. With no keys and no verified attestations, lines **48–50** reject the audit as unsupported. Thus an empty 200 list does not provide missing-signature reporting either. The pinned [npm-registry-fetch 20.0.1 error constructor](https://github.com/npm/npm-registry-fetch/blob/v20.0.1/lib/errors.js#L23-L34) maps HTTP status to `E<status>`.

**[S]** CLI lockfiles pin [pacote **21.5.1** for npm 11](https://github.com/npm/cli/blob/d12b9434dd010b5fb7044c3cc149cdda317813f8/package-lock.json) and [**22.0.0** for npm 12](https://github.com/npm/cli/blob/ae98c62586b6fea44114f72e249e246a2981de70/package-lock.json). Their `lib/registry.js` implementations agree: [21.5.1 / `d36266f5e400`](https://github.com/npm/pacote/blob/d36266f5e4001cffdf70de242b6e825cef06f0c1/lib/registry.js#L176-L224), [22.0.0 / `e4e44c5428c8`](https://github.com/npm/pacote/blob/e4e44c5428c840d853b5ef8641e169b5b79328ab/lib/registry.js#L176-L224). Lines **183–184** choose the first matching `keyid`; missing keys and expired keys throw. The signature verifies SHA-256 over the selected manifest's `<name>@<version>:<integrity>`. Every listed signature is checked. Verification fetches full metadata, lines **121–125**. Keys cannot be substituted independently from their winning version's signatures/integrity.

## Implementation consequences and remaining inferences

| Case | Contract for NPM-04 |
|---|---|
| Signed plus unsigned members | **[D]/[S]** Preserve available keys and winning-version signatures so native npm identifies the unsigned versions. |
| Key aggregation | **[I]** Union all readable eligible members in the shared effective member order; retain one complete entry per `keyid`, first occurrence wins, even when a later entry differs in key bytes or expiry. Pacote's first-match behavior supports this choice but does not prove Artifactory's collision policy. |
| No keys anywhere | **[I]** Return 404, matching existing unsigned local/remote answers. Historical RTDEV-42377 does not establish current all-unsigned behavior. |
| Missing remote keys document | **[I]** Pass over genuine upstream 404, negative-cache misses and offline-without-cache misses; keep other members' keys. |
| Failing remote | **[I]** Retain the direct source answer: 502/504, relayed upstream 401/403/429/5xx, and genuine storage faults, preserving authentication/rate-limit failures. Do not turn these into member misses. **[S]** Native npm rethrows 401/403/429/5xx, but **400 is a client exception**: it suppresses 400 exactly like 404. Faithful 400 propagation cannot promise that npm fails at key discovery. |
| Permissions and policy | **[D]/[I]** `.npm/keys.json` is the documented pattern path; exact virtual/member Read checks and Read-before-policy order follow decisions 0032/0034 and R3/R5/R10. Skip unreadable, blacked-out or member-policy-refused repositories; refuse the virtual before fetching any member. Upstream 401/403 remains a source failure, distinct from caller permission refusal. |
| Offline/cache | **[D]** [Remote offline mode](https://docs.jfrog.com/artifactory/docs/remote-repositories#handling-offline-scenarios) uses cached artifacts without upstream requests. **[I]** Apply the existing remote keys retrieval/cache policy per member and merge per request, with no virtual keys cache. |

**[S]** Current [`npm/virtual.go`](../../internal/artifacts/ecosystem/npm/virtual.go) retains each winning manifest whole; [`http/virtual.go`](../../internal/artifacts/ecosystem/npm/http/virtual.go), `virtualVersion`, changes its tarball URL while retaining the remaining `dist` fields. **[I]** That preserves signatures from the winning member without re-signing or combining losing-version signatures.

The inspected official [OSS **7.161.26** archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz) contains no npm keys route or signing merger; it does not settle these gaps. **#216 must compare:** current empty-union status/body; unreadable/pattern-refused/nested member contribution; duplicate/different-key collision ownership and ordering; every upstream status, malformed document and timeout; cached/offline/no-store keys; and exact winner-signature behavior across duplicate versions and representations. These remain inferences, not vendor captures.

The current application's shared source [safeError](../../internal/artifacts/core/remote/source/source.go)
relays 401/403/429 and 5xx, while other unexpected upstream errors such as
400/410 become its 502. NPM-04 preserves that established direct source answer;
it does not broaden shared status handling. The client 400 exception above is
therefore a limit on final HTTP 400 behavior, not a reason to transform this
adapter's existing source 502 into 400.
