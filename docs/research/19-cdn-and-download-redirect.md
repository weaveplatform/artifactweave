# CDN delivery and download redirect

Checked against: Artifactory 7.161.32

Research baseline: **2026-10-14**.

Artifactory can answer a download with a redirect, either straight to the cloud object
store holding the binary (`downloadRedirect`, "direct cloud storage download") or to a CDN
in front of it (`cdnRedirect`, "CDN distribution"). This report records what Artifactory
documents for both, how the clients that matter follow a redirect, what the three cloud
CDNs offer for signed, time-limited access to a private bucket, and what artifactweave does
today. Decision [0029](decisions/0029-cdn-delivery-and-download-redirect.md) records the
design that follows.

Each fact carries its basis:

| Label | Means |
|---|---|
| **doc** | Documented by the vendor, at the cited page |
| **src** | Seen in source code, at the cited path and commit |
| **inf** | Inferred from the cited facts; not stated anywhere |
| **run** | Observed against the real service, in the cited real-cloud run |

## 1. Artifactory

### 1.1 Direct cloud storage download (`downloadRedirect`)

| Fact | Basis | Source |
|---|---|---|
| Supported on self-hosted Enterprise+ and Edge licences when binaries are on S3, Google Cloud Storage or Azure Blob Storage, and on all AWS and GCP cloud subscriptions | doc | [direct-download] |
| Enabled on the binary provider in `binarystore.xml` with `enableSignedUrlRedirect=true`, on the `s3-storage-v3`, `google-storage-v2` and `azure-blob-storage-v2` templates | doc | [dd-step1] |
| From 7.84.10 S3 direct download no longer works with the `eventual` and cluster providers; JFrog points to the `s3-storage-v3-direct` template | doc | [rn-78410] |
| `signedUrlExpirySeconds` sets the signed URL's lifetime: "The default is 30 seconds" | doc | [dd-step1] |
| Enabled per repository with `downloadRedirect` (UI "Enable Redirect Download"), default `false`; "there is no global configuration to redirect all requests automatically" | doc | [dd-step2] |
| Supported on local, remote, virtual and generic repositories. A remote repository must have `storeArtifactsLocally=true`, otherwise Artifactory reports an error | doc | [dd-step2] |
| A virtual repository redirects only when the local or remote repository that "ultimately provides the requested artifact" has redirection enabled; otherwise Artifactory serves it | doc | [dd-step2] |
| Clients that do not follow a 302 are served by Artifactory as usual | doc | [dd-step2] |
| "Minimum Direct Cloud Storage Download Size": "the default for self-hosted customers is 1 MB". Smaller files are served by Artifactory | doc | [dd-step3] |
| Package types: Generic, Go, Helm, Helm OCI, Docker, Maven, RPM, npm, Debian (client 9 or later), PyPI, Bower, CRAN, Composer, Conan, Gradle, Vagrant, Git LFS and Hugging Face | doc | [direct-download] |
| curl, wget and Gradle need a "supported client version"; firewalls must allow the storage and CDN domains | doc | [direct-download], [dd-step5] |

### 1.2 CloudFront on self-hosted Artifactory

| Fact | Basis | Source |
|---|---|---|
| Self-hosted CloudFront "is relevant only when using AWS S3 storage": it is configured on the S3 provider ("Amazon S3 Official SDK Template") | doc | [dd-step1] |
| Settings: `cloudFrontDomainName`, `cloudFrontKeyPairId`, `cloudFrontPrivateKey` (an `RSA PRIVATE KEY` PEM) and `useSignature` in the example | doc | [dd-step1] |
| The distribution needs "Forward Query Strings = yes" | doc | [dd-step1] |
| The documented key pair is a CloudFront key pair ID, not a trusted key group | doc | [dd-step1] |
| On self-hosted Artifactory CloudFront is part of direct download: "By default, if CloudFront is configured and is a valid binary provider, it should download from it" | doc | [dd-step4] |

### 1.3 CDN distribution (`cdnRedirect`)

| Fact | Basis | Source |
|---|---|---|
| "CDN is supported on AWS only, including AWS marketplace customers"; every AWS region except North California and China | doc | [cdn-saas] |
| "This feature is supported on the Cloud (SaaS) platform, with an Enterprise X or Enterprise+ license" | doc | [cdn-saas] |
| The repository API says the flag "applies to Artifactory Cloud and requires an Enterprise or Enterprise+ license" | doc | [createrepository] |
| JFrog's OpenAPI defines `cdnRedirect` and `downloadRedirect` as strings on local and federated repositories only; Artifactory sends booleans ([research 03](03-repository-configuration-model.md#typing-traps)) | doc | [createrepository] |
| The SaaS feature table has separate "CDN for Public Content" and "CDN for authenticated users" rows whose tier icons differ; the page states no rule in text | doc | [cdn-saas] |
| JFrog's KB says SaaS CDN redirects packages "larger than 200KB" | doc | [cdn-kb] |

### 1.4 The redirect request header

`X-JFrog-Download-Redirect-To` applies to self-hosted Artifactory only [dd-step4] (doc):

| Value | Meaning |
|---|---|
| `None` | "Downloads will occur through Artifactory, skipping direct download and without signedUrl redirect" |
| `S3` | "Downloads occur from S3 even if CloudFront exists" |
| `CF` | "Downloads occur from CloudFront if it is available" |

No other values are documented.

### 1.5 Terraform provider

Read at `jfrog/terraform-provider-artifactory@990e8dfdb64e` (master after v12.11.14):

| Fact | Basis | Source |
|---|---|---|
| `download_direct` maps to `downloadRedirect`; `cdn_redirect` to `cdnRedirect` with no `omitempty`. Both are booleans, default `false`, described as "Available in Enterprise+ and Edge licenses only"; `cdn_redirect` "will redirect the client to download the artifact directly from AWS CloudFront" | src | `pkg/artifactory/resource/repository/local/local.go` |
| Remote repositories send `cdnRedirect` as a non-pointer `bool` on every request | src | `pkg/artifactory/resource/repository/remote/remote.go:831` |
| The remote acceptance test notes that `cdn_redirect` "even when set to true, it comes back as false on the wire (presumably unless testing against a cloud platform)" | src | `pkg/artifactory/resource/repository/remote/resource_artifactory_remote_repository_test.go:331` |

## 2. Clients following a redirect

### 2.1 Go's HTTP client (the JFrog CLI and the Terraform providers)

| Fact | Basis | Source |
|---|---|---|
| The client forwards every header of the initial request except `Authorization`, `WWW-Authenticate` and `Cookie`, which it drops unless the target is the same domain or a subdomain of it | doc | [go-http-client] |
| The comparison uses `URL.Hostname()`, so the port is ignored: `localhost:8081` and `localhost:9000` count as the same host | src | Go 1.27.1 `src/net/http/client.go:1017-1039` (`shouldCopyHeaderOnRedirect`) |
| A credential in a header Go does not treat as sensitive, such as `X-JFrog-Art-Api`, is forwarded to any redirect target | inf | as above |

### 2.2 The JFrog CLI

Read at `jfrog/jfrog-client-go@6053434e9301` (master after v1.55.0):

| Fact | Basis | Source |
|---|---|---|
| The HTTP client follows GET redirects with Go's default policy; POST redirects are captured and re-sent by hand | src | `http/httpclient/client.go` (`doRequest`) |
| A file is downloaded in parts when it is larger than `MinSplitSize`×1000 bytes; the default is 5120, so 5,120,000 bytes, with `SplitCount` 3 | src | `artifactory/services/download.go:431,693` |
| Before a split download it calls `IsAcceptRanges`, which sends a HEAD with `followRedirect=true`, requires 200 and checks `Accept-Ranges: bytes` exactly | src | `artifactory/services/download.go:474`; `http/httpclient/client.go` (`SendHead`, `IsAcceptRanges`) |
| A HEAD that follows a redirect to a URL presigned for GET gets 403 from S3 (SigV4 signs the method) and GCS V4, so the download fails | inf | as above; [research 18 section 1.4](18-cloud-blob-storage.md#14-signed-urls-and-authentication) |
| Each part of a split download is its own ranged GET through the registry | src | `artifactory/services/download.go` (`DownloadFileConcurrently`) |
| `DirectDownloadService.validateChecksumFromHeaders` reads `X-Checksum-*` from the final response and returns success when none is present, so checksum verification is skipped after a redirect to storage | src | `artifactory/services/directdownload.go:677-684` |
| `jf rt dl` checks downloads against the checksums its AQL query returned, not response headers | src | [research 09](09-jfrog-cli-compatibility.md) |

### 2.3 Other clients

| Fact | Basis | Source |
|---|---|---|
| OCI clients "MUST NOT forward `Authorization` headers across host boundaries unless explicitly configured to do so" | doc | [oci-spec] line 829 |
| npm (make-fetch-happen) deletes `authorization` when a redirect changes host | src | `npm/make-fetch-happen` `lib/fetch.js:64-68` |
| Older curl, wget and Gradle send the registry's credentials to the storage host as well as the signed query, which storage rejects with 400 "Only one auth mechanism allowed"; JFrog lists curl 7.58.0, wget 1.20.3 and Gradle 5.6 as the fixed versions | doc | [kb-400] |

## 3. Cloud CDNs for private, signed delivery

### 3.1 Amazon CloudFront

| Fact | Basis | Source |
|---|---|---|
| Origin access control (OAC) is recommended over origin access identity: it supports SSE-KMS, every region, and signs every origin request when set to `always`. The bucket policy allows `cloudfront.amazonaws.com` with `AWS:SourceArn` set to the distribution | doc | [cf-oac] |
| Signers: trusted key groups are recommended over root-user CloudFront key pairs; keys are RSA 2048 or ECDSA 256 PEM; up to five keys per group and four groups per distribution; rotate by adding the new key and removing the old once its URLs expire | doc | [cf-signers] |
| Once a signer is on a cache behaviour, CloudFront rejects requests to it without a valid signed URL or cookie | doc | [cf-signers] |
| Canned policies carry only an expiry; custom policies add a start time, an IP range and wildcard resources, at the cost of a longer URL | doc | [cf-signed-urls] |
| Expiry is checked "at the time of the HTTP request": a download started before expiry completes, but "any GET request that occurs after the expiration time passes will fail", including later ranges | doc | [cf-signed-urls] |
| CloudFront removes `Expires`, `Key-Pair-Id`, `Policy` and `Signature` before forwarding the rest of the URL to the origin | doc | [cf-query-strings] |
| The default cache key is the distribution domain and the URL path; no query string is in it unless a cache policy adds it | doc | [cf-cache-key] |
| Whether signature parameters enter the cache key when a cache policy includes all query strings is not documented | inf | [cf-cache-key], [cf-query-strings] |
| Go: `github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign` offers `NewURLSigner(keyID, crypto.Signer)`, `Sign` (canned) and `SignWithPolicy`, cookie signers and PEM loaders (`LoadPEMPrivKey` for PKCS#1, `LoadPEMPrivKeyPKCS8AsSigner`) | doc | [cf-go-sign] |
| SHA-1 is the default hash; `HashSHA256` (since v1.11.0) "requires appending `Hash-Algorithm=SHA256`" to the URL | doc | [cf-go-sign] |
| Largest cacheable GET response: 50 GB | doc | [cf-limits] |

### 3.2 Google Cloud CDN

| Fact | Basis | Source |
|---|---|---|
| Signed URL format: `?Expires=…&KeyName=…&Signature=…`; HMAC-SHA1 over the URL with the raw 16-byte key, base64url-encoded; optional `URLPrefix` signs every path under a prefix | doc | [gcdn-signed] |
| Keys are 128 bits of randomness, base64url; "Each backend can have up to three keys configured at a time" | doc | [gcdn-signed] |
| "Cloud CDN doesn't block requests without a `Signature` query parameter or `Cloud-CDN-Cookie` HTTP cookie"; it rejects invalid or malformed signatures. Unsigned cache misses are forwarded to the origin | doc | [gcdn-signed] |
| Cloud Storage must be restricted "so that Cloud Storage can reject requests that are missing a valid signature": remove public access and grant `roles/storage.objectViewer` to `service-PROJECT_NUMBER@cloud-cdn-fill.iam.gserviceaccount.com` | doc | [gcdn-signed] |
| That service account "is created automatically the first time that you add a key to a backend bucket in a project", so the grant must follow the key | doc | [gcdn-signed] |
| Which identity fetches an unsigned cache miss is not stated; that a private bucket rejects it is the stated purpose of the restriction | inf | [gcdn-signed] |
| "Responses to signed and unsigned requests are cached separately", and the signature parameters are removed before the request reaches the origin | doc | [gcdn-signed] |
| Whether `Expires`, `KeyName` and `Signature` are part of the cache key is not stated | inf | [gcdn-signed] |
| Signed responses are cached for `signed-url-cache-max-age`, which "defaults to one hour" | doc | [gcdn-signed] |
| "Private bucket access" makes a private bucket reachable through Cloud CDN without signed URLs, so it is not viewer authorisation | doc | [gcdn-private] |
| There is no Go SDK helper for signing; Google's page gives a Go sample | doc | [gcdn-signed] |
| The signed string is the whole URL, scheme to `KeyName`, query included: Google's Go sample appends `Expires` and `KeyName` with `?`, or `&` when the URL already has a query, and its comment asks only that the URL carry no `Expires`, `KeyName` or `Signature` | doc | [gcdn-signed] |
| The signature is base64url with its padding; "Don't convert trailing `=` characters of the signature to their percent-encoded form" | doc | [gcdn-signed] |
| `URLPrefix` signs `URLPrefix=…&Expires=…&KeyName=…`, and its prefix is matched "as a text substring, not strictly a directory path" | doc | [gcdn-signed] |
| A key name is 1 to 63 characters, `[a-z]([-a-z0-9]*[a-z0-9])?` (RFC 1035); the key value is 128 bits, RFC 4648 base64url | doc | [gce-signedurlkey] |
| A backend bucket's cache key leaves out protocol and host, and "By default, only query parameters that are known to Cloud Storage are included"; an include list can add parameters, and "Cloud CDN doesn't support excluding query parameters from a cache key to a backend bucket" | doc | [gcdn-caching], [gcdn-cache-keys] |
| Cloud Storage's `response-content-disposition` and `response-content-type` override those headers "for authenticated GET requests" | doc | [gcs-xml-headers] |
| Cloud CDN forwards both override parameters to the backend bucket on a cache fill, which Google does not document: a fresh fill for a signed URL carrying them returned `Content-Disposition: attachment; filename="fill-check.json"` and `Content-Type: application/json` (2026-10-16). `gcp/cdn` keys its cache on both explicitly, so whether they count among the parameters "known to Cloud Storage" no longer matters | run | [gcp-cdn-proof], [gcdn-caching], [gcs-xml-headers] |
| A Cloud CDN backend bucket sits behind a global external Application Load Balancer, which needs a certificate for HTTPS | doc | [gcdn-signed], [glb-pricing] |

### 3.3 Azure Front Door

| Fact | Basis | Source |
|---|---|---|
| Azure CDN from Edgio shut down on January 15, 2025 | doc | [edgio-faq] |
| Token authentication at the edge was a feature of Azure CDN Premium from Edgio only | doc | [azure-token-auth] |
| Azure CDN Standard from Microsoft (classic) retires on September 30, 2027 and has accepted no new resources since August 15, 2025; Front Door (classic) retires on March 31, 2027 | doc | [classic-cdn-faq], [afd-caching] |
| Front Door Standard and Premium validate no signature or token. To protect blobs, Microsoft advises a SAS in the URL and "*Use Query String* behavior to prevent Azure Front Door from serving requests to unauthenticated clients", at a cost in cache efficiency | doc | [afd-storage] |
| Premium reaches a storage account over Private Link, so the account can deny public network access; Standard needs the account publicly reachable | doc | [afd-storage] |
| The azurerm route's `query_string_caching_behavior` "Defaults to `IgnoreQueryString`", under which a cached object is served to later requests whatever their query string | doc | [azurerm-route], [afd-caching] |
| With no `Cache-Control` from the origin, "by default Front Door randomly determines a cache duration between one and three days"; a rules-engine cache override can replace it ("Override always", "Override if origin missing") | doc | [afd-caching] |
| "Front Door might be able to serve data from the cache even if the cached data has expired or if the origin is returning error responses" | doc | [afd-caching] |
| Only GET is cached; files are fetched from the origin in 8 MB chunks, which needs correct range support at the origin | doc | [afd-caching] |
| For some minutes after a Premium profile with a Private Link origin first answers, a download of more than 8 MiB can be served for its first 8 MiB and then reset (HTTP/2 `INTERNAL_ERROR`) while the edge fetches the next chunk; the same download passes once a few more minutes have gone by. The account itself answers every range and validator request correctly throughout (`206`, consistent `Last-Modified` and ETag on an LRS account, with `If-Range`, `If-Unmodified-Since` and `If-Match`). Most likely Front Door's configuration still spreading to where chunks are fetched | run | [azure-cdn-proofs] |
| A service or user delegation SAS can override the response's `Cache-Control` (`rscc`), `Content-Disposition` (`rscd`), `Content-Encoding` (`rsce`), `Content-Language` (`rscl`) and `Content-Type` (`rsct`); the overrides are part of the signed string | doc | [service-sas] |
| SAS start and expiry are explicit (`st`, `se`), so a signer that fixes both gets the same URL for the same blob and key | inf | [service-sas] |
| Microsoft's Premium reference deployment with Private Link exists for Bicep, ARM and Terraform | doc | [afd-storage] |

### 3.4 Testing

| Fact | Basis | Source |
|---|---|---|
| None of the three CDNs has an emulator | inf | searches during planning, 2026-10-14 |
| LocalStack needs an auth token from 2026.03.0, and whether it enforces CloudFront signatures is unconfirmed | doc / inf | [localstack-2026-03] |
| Azurite does not support response header overrides in a service SAS | src | Azurite README and ChangeLog |

## 4. Costs

Read on 2026-10-14; list prices in USD, first tier.

| Service | Price | Source |
|---|---|---|
| CloudFront | $0.085/GB to viewers in the US and EU for the first 10 TB; $0.0100 per 10,000 HTTPS requests; the first 1 TB and 10 million requests each month are free; origin fetches from AWS are free; 1,000 invalidation paths a month free, then $0.005 per path | [cf-pricing] |
| Cloud CDN | cache egress $0.02 to $0.20/GiB by region; cache fill $0.01 to $0.04/GiB; $0.0075 per 10,000 lookups | [gcdn-pricing] |
| External Application Load Balancer | the first five forwarding rules $0.025/hour | [glb-pricing] |
| Front Door | base fee $35/month (Standard) or $330/month (Premium); $0.083/GB edge to client for the first 10 TB; $0.02/GB edge to origin; $0.009 (Standard) or $0.015 (Premium) per 10,000 requests | [afd-pricing] |

## 5. artifactweave today

At first written for main at `00888fc`, after #139; updated for #146 (the redirect policy),
#120 (CloudFront), #121 (Cloud CDN) and #122 (Front Door).

| Behaviour | Source |
|---|---|
| A GET on a repository with `downloadRedirect` or `cdnRedirect` follows decision 0029's policy: conditional requests that resolve to 304 or 412 are answered locally; a repository with neither flag, a file under `download.redirect.min_size` (1 MiB) and `X-JFrog-Download-Redirect-To: None` stream; otherwise the store's CDN unless the header says `S3`, then storage for `downloadRedirect`, then stream | `internal/server/redirect.go` (`route`, `redirect`) |
| HEAD is answered locally, never redirected | `internal/server/redirect.go` |
| The URL lives `download.redirect.ttl`, 5 minutes by default; a CDN URL expires at the end of its TTL-long window plus the TTL | `internal/config`, `pkg/cdn` (`Expiry`) |
| A 302 carries no `Content-Type`, `Content-Disposition` or `ETag`; the signed URL carries the name and media type as response overrides | `internal/server/redirect.go`, each backend's `SignURL` |
| A redirected download is counted when it is a whole file or a range starting at byte 0, and `skipUpdateStats=true` opts out | `internal/server/redirect.go` (`countsDownload`) |
| A signing failure is logged at WARN at most once a minute and the download streams | `internal/server/redirect.go` (`signFailed`) |
| `blob.Signer` is `Signs(via)`, fixed when the store is built, and `SignURL(ctx, sha256, SignOptions)`. Storage URLs: s3 with its presign client; gcs when its credentials can sign; azure unless it runs on a pre-issued SAS; fs never. CDN URLs: s3 with a CloudFront configuration; gcs with a Cloud CDN configuration; azure with a Front Door, unless it runs on a pre-issued SAS | `pkg/blob/blob.go`, `pkg/blob/{s3,gcs,azure}` |
| CloudFront URLs are canned-policy signed with `aws-sdk-go-v2/feature/cloudfront/sign`, SHA-1 or SHA-256, with an RSA or ECDSA key read from a file at start | `pkg/cdn/cloudfront.go`, `internal/app/blob.go` |
| Cloud CDN URLs are signed whole, not by `URLPrefix`: `Expires` and `KeyName` appended, then an HMAC-SHA1 `Signature` with the 128-bit key read from a base64url file at start. A test checks the signer against vectors computed with Google's Python sample | `pkg/cdn/cloudcdn.go` (`SignCloudCDN`), `pkg/cdn/cloudcdn_test.go`, `internal/app/blob.go` |
| Front Door URLs are the store's own read SAS, by shared key or user delegation, with start and expiry fixed to the TTL-long window, `rscd`/`rsct` overrides and `rscc: public, max-age=<TTL>`, moved onto the endpoint host. One delegation key is kept across a window, so the window's URLs are identical; a SAS that would outlive the longest key (7 days less an hour) is refused | `pkg/blob/azure` (`signFrontDoor`, `delegationSAS`), `pkg/cdn/frontdoor.go` |
| A mirror signs with the first member in read order that holds the blob and signs the route, without counting signing against the member's health | `pkg/blob/mirror/read.go` (`SignURL`) |
| The server refuses to start with a CDN on the archive tier, or on the `base_url` host or a subdomain of it | `internal/config/config.go` (`validateCDN`) |
| A fake edge checks CloudFront or Cloud CDN signatures and serves blobs from a store; acceptance runs `jf rt dl` of a split download through each, with MinIO or fake-gcs-server behind | `pkg/cdn/cdntest`, `test/acceptance/features/cdn.feature` |
| A fake Front Door checks nothing, passes the SAS through to its storage origin and caches whole-object responses under the full URL up to a cap; acceptance runs a split `jf rt dl` and a repeated download through it with Azurite behind, which checks the SAS and applies the overrides. Azurite refuses an anonymous read with 403 `AuthorizationFailure`. Not modelled: the route's real cache behaviour (8 MiB chunks, ranged responses) and Private Link | `pkg/cdn/cdntest/frontdoor.go`, `test/acceptance/features/cdn_front_door.feature` |

## Artifactory 7.x check

Checked on 2026-10-17 against the self-managed release notes up to 7.161.32 and the
legacy notes for 7.0 to 7.83. Section 1 was corrected in place.

| Claim | 7.x | Source |
|---|---|---|
| Direct cloud storage download on S3, GCS and Azure with `enableSignedUrlRedirect` and `signedUrlExpirySeconds` (30 s) | Confirmed. Changed for S3: not with the `eventual` and cluster providers from 7.84.10 | [dd-step1], [rn-78410] |
| `X-JFrog-Download-Redirect-To` values `None`, `S3`, `CF` | Confirmed; 7.19.4 introduced the choice between S3, CloudFront and no signed-URL redirect | [dd-step4], [rn-legacy] (7.19.4) |
| Signed URLs and the signed URL key | Confirmed; introduced in 7.5.0 with the Create Signed URL and Replace Signed URL Key APIs | [rn-legacy] (7.5.0) |
| `cdnRedirect` is SaaS CloudFront for Enterprise X and Enterprise+ | Confirmed; CDN distribution is on by default for SaaS Enterprise and Enterprise+ from 7.5 | [cdn-saas], [rn-legacy] (7.5.0) |
| The redirect URL keeps the item's media type | Confirmed: 7.84.10 fixed CDN downloads whose content type held a `+`, which the redirect URL had dropped (RTDEV-40507). artifactweave's blob contract signs only `application/x-test`, so a `+` media type is not covered by a test | [rn-78410], `pkg/blob/blobtest` |
| Which shard a signed URL comes from | Changed in [research 18](18-cloud-blob-storage.md#31-how-artifactory-does-it): the sharding provider's zone read order, which is what artifactweave's mirror does | [rn-78410] |

## Versions at the research baseline

| Component | Version | Note |
|---|---|---|
| JFrog Artifactory docs | 7.x, read 2026-10-14 | docs.jfrog.com pages fetched as markdown |
| Artifactory release notes | self-managed up to 7.161.32, read 2026-10-17 | [Artifactory 7.x check](#artifactory-7x-check) |
| jfrog-client-go | master `6053434e9301` (after v1.55.0) | `download.go`, `directdownload.go`, `httpclient/client.go` |
| `jfrog/artifactory` Terraform provider | master `990e8dfdb64e` (after v12.11.14) | |
| Go | 1.27.1 | `net/http` redirect policy |
| `aws-sdk-go-v2/feature/cloudfront/sign` | v1.12.5 (2026-10-08) | |
| `hashicorp/azurerm` provider docs | main, read 2026-10-14 | `cdn_frontdoor_route` |
| OCI distribution-spec | main | `spec.md` line 829 |

## Sources

**JFrog**
- [rn-78410]: https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed
- [rn-legacy]: https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf
- [direct-download]: https://docs.jfrog.com/installation/docs/direct-cloud-storage-download
- [dd-step1]: https://docs.jfrog.com/installation/docs/step-1-configure-the-artifactory-filestore-for-direct-cloud-storage
- [dd-step2]: https://docs.jfrog.com/installation/docs/step-2-specify-repositories-for-direct-download
- [dd-step3]: https://docs.jfrog.com/installation/docs/step-3-finetune-direct-download
- [dd-step4]: https://docs.jfrog.com/installation/docs/step-4-control-your-signed-url-downloads
- [dd-step5]: https://docs.jfrog.com/installation/docs/step-5-configure-firewalls
- [cdn-saas]: https://docs.jfrog.com/administration/docs/jfrog-cloud-with-cdn-distribution
- [cdn-kb]: https://jfrog.com/help/r/artifactory-cdn-redirect-in-jfrog-cloud/artifactory-cdn-redirect-in-jfrog-cloud
- [createrepository]: https://docs.jfrog.com/artifactory/reference/createrepository
- [kb-400]: https://jfrog.com/help/r/artifactory-how-to-resolve-a-400-bad-request-error-when-trying-to-download-or-resolve-artifacts

**Clients**
- [go-http-client]: https://pkg.go.dev/net/http#Client
- [oci-spec]: https://github.com/opencontainers/distribution-spec/blob/main/spec.md

**AWS**
- [cf-oac]: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-restricting-access-to-s3.html
- [cf-signers]: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-trusted-signers.html
- [cf-signed-urls]: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-signed-urls.html
- [cf-query-strings]: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/QueryStringParameters.html
- [cf-cache-key]: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/understanding-the-cache-key.html
- [cf-go-sign]: https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign
- [cf-limits]: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/cloudfront-limits.html
- [cf-pricing]: https://aws.amazon.com/cloudfront/pricing/pay-as-you-go/
- [localstack-2026-03]: https://blog.localstack.cloud/localstack-for-aws-release-2026-03-0/

**Google Cloud**
- [gcdn-signed]: https://docs.cloud.google.com/cdn/docs/using-signed-urls
- [gcdn-private]: https://docs.cloud.google.com/cdn/docs/configure-private-bucket-access
- [gcdn-caching]: https://docs.cloud.google.com/cdn/docs/caching
- [gcdn-cache-keys]: https://docs.cloud.google.com/cdn/docs/using-cache-keys
- [gce-signedurlkey]: https://docs.cloud.google.com/compute/docs/reference/rest/v1/backendBuckets/addSignedUrlKey
- [gcs-xml-headers]: https://docs.cloud.google.com/storage/docs/xml-api/reference-headers
- [azure-cdn-proofs]: https://github.com/weaveplatform/artifactweave/actions/runs/38925440868 (the `azure/cdn` proofs of #258, 2026-10-16: runs 38906963602 and 38916559470 failed at 8 MiB, 38925440868 passed a few minutes later with the account's range and validator answers logged)
- [gcp-cdn-proof]: https://github.com/weaveplatform/artifactweave/actions/runs/38897773729 (the `gcp/cdn` proof of #257, `test/platform`'s cache-fill check, 2026-10-16)
- [gcdn-pricing]: https://cloud.google.com/cdn/pricing
- [glb-pricing]: https://cloud.google.com/load-balancing/pricing

**Azure**
- [edgio-faq]: https://learn.microsoft.com/en-us/azure/cdn/edgio-retirement-faq
- [azure-token-auth]: https://learn.microsoft.com/en-us/azure/cdn/cdn-token-auth
- [classic-cdn-faq]: https://learn.microsoft.com/en-us/azure/cdn/classic-cdn-retirement-faq
- [afd-storage]: https://learn.microsoft.com/en-us/azure/frontdoor/scenario-storage-blobs
- [afd-caching]: https://learn.microsoft.com/en-us/azure/frontdoor/front-door-caching
- [azurerm-route]: https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_route
- [service-sas]: https://learn.microsoft.com/en-us/rest/api/storageservices/create-service-sas
- [afd-pricing]: https://azure.microsoft.com/en-us/pricing/details/frontdoor/
