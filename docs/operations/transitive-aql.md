# Smart Remote transitive AQL

A configured Smart Remote pointing at a local repository on its peer can search
upstream-only artifacts using `items.find({"repo":"smart"}).transitive()`.
Ordinary AQL sees local cached items. Transitive search adds upstream rows without
creating items or blobs, so an uncached `smart-cache/path` still returns 404.

For the pinned JFrog CLI 2.126.0:

```sh
jf rt search 'smart/*.bin' --transitive=true --server-id registry
JFROG_CLI_TRANSITIVE_DOWNLOAD=true jf rt download 'smart/*.bin' ./ --flat --fail-no-op --server-id registry
```

The pinned CLI accepts the search flag. Its download command uses the environment
setting or a file spec's `transitive` field; its flag table omits `--transitive`, even
though the current JFrog guide documents it. The native acceptance scenario exercises
this actual client without rewriting returned paths or first warming the cache.
It verifies returned checksums through a cold download, an ordinary warm-cache query,
and a later cached download with the peer unavailable.

An upstream-only row reports the normal local remote key, enabling ordinary GET.
Actual cache rows report `-cache`. Existing cached paths take precedence over peer
metadata, including when that cache metadata does not satisfy the query. This avoids
advertising upstream checksums for bytes the cache would deliver differently. These
mapping/precedence choices are explicit vendor inferences pending #216 capture.

Queries require one repository equality in every alternative, Items as the primary
domain, and only item/property includes. Sort and offset are rejected. A virtual
query searches the first five distinct remote members in configured depth-first
order; all member caches/local items remain eligible. Member read grants and all
traversed virtual include/exclude patterns constrain disclosure. No virtual grant
implicitly grants access to a member. Administrators retain AQL's documented pattern
exemption, while ordinary downloads still enforce their existing rules.

The operation retains the original and current user/token authority, peer credential
revision and source identity through delivery. Peer HTTP requests reuse the shared
security/connection/capacity controls, carry configured upstream credentials, and
never follow redirects. Search results have no persistent cache or usage statistics.
Offline or failed selected peers fail the entire query instead of yielding an
incomplete success. The response is private transient metadata (`Cache-Control:
no-store`); static errors never reflect a peer URL, body or credential.

Implementation safety limits are one minute per query, 8 MiB per peer JSON response,
10,000 returned rows narrowed by existing AQL settings, and one extra row
per source for detecting trimming. There are at most five peer searches; traversal
is limited to eight virtual levels and 256 nodes. Malformed ranges, conflicting
row duplicates and partial-result notifications fail closed. Unsupported query or
source shapes return 400, changed/revoked authority 403, timeout 408 and remaining
source failures 502. Exact statuses, limits and nested ordering remain [I]/#216.
P12 owns the configurable member-limit system property; P10 owns native package
virtual resolution. See [ADR0012](../research/decisions/0012-search-and-aql.md) and
[Smart discovery configuration](smart-remote.md).
