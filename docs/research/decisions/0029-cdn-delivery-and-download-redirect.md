# 0029: CDN delivery and download redirect

Status: Proposed

## Context

Artifactory can answer a download with a 302 instead of streaming the bytes, in two ways
([research 19](../19-cdn-and-download-redirect.md) section 1):

- **Direct cloud storage download.** A repository with `downloadRedirect` sends the client
  to a signed URL on the object store. Self-hosted, Enterprise+ or Edge, on S3, GCS and
  Azure, enabled on the binary provider, 30-second URLs by default, files under 1 MB
  served directly. On the S3 provider a CloudFront distribution can front the bucket, and
  then direct download goes to CloudFront by default.
- **CDN distribution.** A repository with `cdnRedirect` sends the client to CloudFront. JFrog
  SaaS only, AWS only, Enterprise X or Enterprise+.

A client can steer the choice with `X-JFrog-Download-Redirect-To: None | S3 | CF`.

artifactweave already redirects `downloadRedirect` GETs to a presigned URL on any store that
can sign (research 19 section 5). Teams leaving Artifactory run on all three clouds and want
CDN delivery there too, on their own instances.

The clients set hard limits. The JFrog CLI sends a HEAD, following redirects and requiring
200, before any download over 5,120,000 bytes, and a URL signed for GET answers HEAD with
403 on S3 and GCS. Go's client forwards `Authorization` to a redirect target on the same
host or a subdomain of it, comparing host names without the port. The CLI skips its header
checksum check after a redirect (research 19 section 2).

## Decision

**Which requests redirect.**
- A GET of a file on `/artifactory/{repo}/{path}` in a local, federated or remote repository,
  or through a virtual repository using the flags of the repository that serves the file.
  Ranged GETs redirect too: each part of a split download is its own GET, and the signed
  URL serves ranges.
- A Docker or OCI blob GET (`/v2/<name>/blobs/<digest>`), when that API is served, through
  the same policy.
- Never HEAD, which is answered locally with the length, `Accept-Ranges` and the checksum
  headers. Never a manifest, a checksum sidecar, an archive entry (`!/`), a folder download
  or a remote repository's cache miss.

**Policy.** One function, `internal/server/redirect.go`, decides for every caller, in this
order:

1. A conditional request whose validators decide the answer (304 or 412) is answered
   locally from the item's metadata.
2. A repository with neither `downloadRedirect` nor `cdnRedirect` streams.
3. A file smaller than `download.redirect.min_size` streams.
4. `X-JFrog-Download-Redirect-To: None` streams.
5. Unless the header is `S3`, a store with a CDN answers 302 to the CDN. `downloadRedirect`
   or `cdnRedirect` is enough, as on self-hosted Artifactory, where CloudFront is part of
   direct download.
6. Otherwise, with `downloadRedirect` and a store that signs, answer 302 to the object
   store. `S3` means "the object store, not the CDN" on every cloud. `CF` changes nothing:
   the CDN is already preferred.
7. Otherwise stream.

Anything that can refuse or reroute a download runs before the policy: the permission
check, `BEFORE_DOWNLOAD` workers ([0026](0026-workers-runtime.md)) and archived items
([0024](0024-cold-storage-tier.md)).

**The 302.**
- It carries `Location`, the `X-Checksum-*` headers, `X-Artifactory-Filename` and
  `Cache-Control: private, no-store`. It carries no artifact `Content-Type`,
  `Content-Disposition` or `ETag`; those describe the body, which the 302 has none of.
- The signed URL carries the item's name and media type as response overrides:
  `response-content-disposition` and `response-content-type` on S3 and GCS, `rscd` and
  `rsct` on Azure.
- A redirected download is counted under the same rule as a streamed one: a whole file, or
  a range starting at byte 0, unless `skipUpdateStats=true`.

**Signing contract.** `pkg/blob` replaces `Presigner` with:

```go
type Via int // ViaStorage or ViaCDN

type SignOptions struct {
	Via         Via
	TTL         time.Duration
	Filename    string
	ContentType string
}

type Signer interface {
	Signs(via Via) bool
	SignURL(ctx context.Context, sha256 string, o SignOptions) (string, error)
}
```

- `Signs` is fixed when the store is built, from its configuration and credentials:
  - s3 signs for storage when it has a presign client, and for the CDN with a CloudFront
    configuration;
  - gcs signs for storage when its credentials can sign or `google_access_id` is set, and
    for the CDN with a Cloud CDN key;
  - azure signs for both unless it runs on a pre-issued SAS token;
  - fs signs nothing;
  - a mirror signs for a route when any member does.
- A repository whose flags ask for a redirect its store cannot sign is logged once at
  start. A signing failure at request time is logged at WARN at most once a minute and the
  download streams.
- The CDN signers live in `pkg/cdn`, one per provider, and are injected through each
  backend's `Options`, so `Signs` is known at construction. Front Door is the exception
  in kind: it checks no signature, so `pkg/cdn.FrontDoor` only places a SAS on the
  endpoint, and the azure store signs the SAS with its own credentials. A store is never wrapped in a
  CDN decorator, because a wrapper hides the store's other interfaces, `blob.PartSizer` in
  particular, which silently changes mirror chunk sizing (#39).
- s3, gcs and azure export `ObjectKey(sha256)`, the object name under the store's prefix,
  for the CDN signers.
- A mirror signs with the first member in read order that holds the blob and `Signs` the
  route. Signing never counts against a member's health.
- A signer whose signing is a network call caches the URLs it signs, per blob, overrides
  and expiry window: at most 4096 per store, the least recently used dropped first. That is
  a gcs store signing through IAM `signBlob`, one request per URL: no local key, and
  credentials that are impersonated, federated, the metadata server's, or named by
  `google_access_id` alone. Local signers cache nothing: s3, gcs with a key, and azure,
  whose user delegation SAS is signed locally with a key fetched about once a week.

**Lifetime.**
- `download.redirect.ttl` sets the minimum time a URL stays valid when handed out:
  5 minutes by default, at most 7 days (the V4 limit).
- Storage URLs expire at issue time plus the TTL, except from a store that caches: those
  expire on windows like CDN URLs, so one signature serves a window. A cached URL is handed
  out only in its own window, so it always has at least the TTL left. A TTL over half the
  V4 limit (3.5 days) is not windowed, so no URL outlives 7 days.
- CDN URLs expire at the end of the current window plus the TTL, where a window is TTL long
  and starts on a multiple of it. Every client in a window gets the same URL for a blob, so
  edge caches keyed on the full URL still hit.

**Configuration.**

| Key | Meaning |
|---|---|
| `download.redirect.min_size` | Smallest file that redirects; default 1 MiB, matching Artifactory's self-hosted 1 MB |
| `download.redirect.ttl` | As above |
| `<backend>.cdn.cloudfront.{domain, key_pair_id, private_key_file, hash}` | On an s3 store: the distribution host, the public key ID in a trusted key group, the PEM key (PKCS#1 or PKCS#8, RSA or ECDSA), and `sha1` (default) or `sha256` |
| `<backend>.cdn.cloud_cdn.{domain, key_name, key_file}` | On a gcs store: the load balancer host, the backend bucket key name, and the base64url key |
| `<backend>.cdn.front_door.endpoint` | On an azure store: the Front Door endpoint host |
| `<backend>.cdn.*.path_prefix` | The origin path, if the CDN's origin is not the bucket root; empty by default |

- The `cdn` block is accepted under `blob.<backend>` and `blob.stores.<name>.<backend>`, and
  refused under `cold_blob`: archived content is not served from an edge.
- The server refuses to start when a CDN host equals the `base_url` host or is a subdomain
  of it, because Go clients would send their `Authorization` header to the CDN.
- Key files are read and parsed at start. Secrets come from files or the environment, never
  the configuration file's values.

**Per CDN.**
- **CloudFront:** a canned policy signed with `aws-sdk-go-v2/feature/cloudfront/sign`.
  The distribution's cache policy keys on the path plus `response-content-disposition`
  and `response-content-type` only, so the signature never fragments the cache.
- **Cloud CDN:** `Expires`, `KeyName` and `Signature` per Google's algorithm, over the
  whole URL with its overrides, so an edited filename fails the signature. Each URL is
  signed whole, never by `URLPrefix`: a prefix signature would open every blob under the
  prefix to whoever holds one URL, and windows already make URLs repeat for the cache. The
  bucket is private, readable only by the `cloud-cdn-fill` account, so an unsigned request
  fails at the origin.
- **Front Door:** SAS passthrough. artifactweave signs a user delegation SAS (or a service
  SAS under a shared key) with `st` and `se` fixed per window, `rscd` and `rsct`, and
  `rscc: public, max-age=<TTL>`. It then replaces the storage host with the Front Door
  endpoint, keeping the path `/<container>/<object key>`.
  - `max-age` is the TTL, not the time left in the window: a value that changed between
    requests would make every SAS in the window a different string and defeat the
    cache. It bounds an edge copy even where the route's cap is missing, since without a
    `Cache-Control` Front Door keeps an object for one to three days.
  - The route must cache with `UseQueryString` and cap cache expiry at the window with a
    rules-engine override.
  - A URL minted in a window is accepted by storage until the window ends plus the TTL.
    An edge copy cached just before then can be served to holders of the same URL for up
    to the cache cap after that. With the defaults, that is at most 15 minutes from minting.

**No licence gate.** Both flags work on every instance and every cloud.

**No invalidation.** Blobs are content-addressed and never change. Deleting an item does
not purge edges:
- CloudFront checks a signature on every request.
- Cloud CDN caches signed and unsigned responses separately.
- Front Door serves a cached copy only for its exact URL, SAS included.

Holders of a URL issued before a delete can fetch the blob until the exposure above ends.

## Rationale

**Fidelity first.** Self-hosted Artifactory sends `downloadRedirect` downloads to CloudFront
when the store has one, and SaaS uses `cdnRedirect` for the CDN. Accepting either flag for
the CDN means a repository migrated from either keeps its delivery path, and the header
keeps its meaning.

**Where it improves on Artifactory** (see the
[deviations register](../../implementation/deviations.md#delivery)):
- **CDNs on every cloud and on self-hosted instances,** with no licence tier. Artifactory
  offers CloudFront only, as a SaaS feature or on the S3 provider.
- **A 5-minute default lifetime instead of 30 seconds.** Artifactory's expiry is checked
  when the request arrives, so a client that retries a dropped connection, or fetches a
  later range of a split download, after 30 seconds fails without coming back to the
  registry. Five minutes covers those retries. A leaked link still expires quickly, and on
  CloudFront, where every request is checked, it expires exactly.
- **HEAD and conditional requests never leave the registry,** so the CLI's split-download
  probe works on every backend and revalidation never fetches a whole file from storage.

**Alternatives considered:**
- *Only `cdnRedirect` selects the CDN* (separate flags for CDN and storage): clearer per
  repository, but a repository migrated from self-hosted Artifactory with CloudFront would
  silently bypass the CDN.
- *Presign HEAD separately*, as CNCF distribution's S3 driver does: a second signature per
  probe and a storage round trip, where the registry already holds the answer.
- *Redirect only whole files and first ranges:* the later parts of a split download would
  stream through the registry, two thirds of the bytes at the CLI's defaults.
- *Signed cookies:* package clients do not keep cookies across hosts.
- *Front Door managed identity to the origin:* no viewer authorisation at all, so private
  content would be public. Rules-engine token checks can only compare a static secret.
- *A CDN decorator around the store:* hides `PartSizer` and other optional interfaces (#39).
- *Invalidating on delete:* a cost per path on CloudFront, an API call per edge network,
  and nothing to protect once signatures are short-lived.

## Constraints

- **Front Door validates nothing.** The exposure bound depends on the route's
  `UseQueryString` and cache cap, which artifactweave cannot check at runtime. The
  reference deployment asserts them. Front Door may also serve an expired cached copy while
  the origin returns errors, so the bound is a design target, not a guarantee.
- **The CloudFront cache policy** must not key on the signature parameters, and must key on
  the override parameters, or downloads either miss the cache or get another item's
  filename. AWS does not document whether signature parameters enter a policy that keys on
  every query string.
- **Cloud CDN overrides:** Cloud Storage honours `response-content-disposition` and
  `response-content-type` on authenticated reads, and a backend bucket's default cache key
  keeps Cloud Storage's own query parameters, but Google does not state that Cloud CDN
  passes them on a cache fill (research 19 §3.2). The reference deployment checks it. If
  they are dropped, the stored object's headers are served; package clients name files
  from the request, not the header.
- **Cloud CDN does not block unsigned requests.** A bucket that stays public, or "private
  bucket access", makes every blob public through the CDN.
- **Temporary credentials** end a storage URL's validity when the session ends, so the TTL
  must stay below the shortest session lifetime of the store's credentials.
- **Clients:** curl before 7.58.0, wget before 1.20.3 and Gradle before 5.6 fail on redirects
  to storage. `X-JFrog-Art-Api` is not a header Go treats as sensitive, so it reaches the
  redirect target.
- **The CLI's header checksum check** is skipped after a redirect. `jf rt dl` still verifies
  against its AQL checksums.
- **Presigned URLs bypass mirror health** once handed out, as [0028](0028-blob-mirroring.md)
  records.
- **A user delegation SAS cannot outlive its key,** which the service issues for at most
  7 days. A Front Door URL lasts up to twice the TTL, so with Entra ID the TTL must keep
  that under 7 days less the hour a key is renewed early; a longer SAS is refused at
  signing.

## Verification

- `internal/server` tests for every policy rule:
  - the threshold;
  - the three header values;
  - the fall-through order;
  - HEAD and conditional requests answered locally;
  - the headers of the 302;
  - download counts.
- `blobtest` covers `Signs` and `SignURL` with overrides on fs, s3 (fake and MinIO), gcs
  (fake and emulator), azure (fake and Azurite) and the mirror.
- `pkg/cdn` tests sign with generated keys and verify with the public key (CloudFront),
  Google's documented algorithm (Cloud CDN) and the SAS string-to-sign (Front Door).
- `pkg/cdn/cdntest`, a chi fake edge, validates each provider's signature and proxies to a
  blob store. Acceptance serves artifactweave on `127.0.0.1` and the edge on `localhost`,
  so Go strips `Authorization`, and runs `jf rt dl` of a file above 5 MiB through it.
- The reference deployments' CDN scenarios check the real edges: signed downloads,
  unsigned and expired requests refused, and the Front Door cache cap.

## References

- [Research 19](../19-cdn-and-download-redirect.md), with sources for every fact above.
- [Research 18 section 1.4](../18-cloud-blob-storage.md#14-signed-urls-and-authentication)
  and [2.5](../18-cloud-blob-storage.md#25-sas-authentication-and-transport): GCS and Azure
  signing.
- Decisions [0004](0004-storage-sql-metadata-and-blobs.md) (the blob contract),
  [0027](0027-cloud-blob-backends.md) (the cloud backends) and
  [0028](0028-blob-mirroring.md) (the mirror).
- `internal/server/redirect.go` and `internal/server/items.go` (`serveFile`).
- `github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign@v1.12.5`.
