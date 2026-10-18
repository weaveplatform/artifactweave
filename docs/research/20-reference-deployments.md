# Reference deployments: multi-region storage, Terraform practice and CI identity

Checked against: Artifactory 7.161.32

Research baseline: **2026-10-14**.

artifactweave runs on fs, S3, GCS and Azure blob stores and can mirror blobs across them
(decisions [0027](decisions/0027-cloud-blob-backends.md) and
[0028](decisions/0028-blob-mirroring.md)). This report collects the facts behind a set of
tested reference deployments, one Terraform root per cloud and scenario, and the CI that
applies, tests and destroys them:

- how each cloud replicates object storage across regions, and how that interacts with
  signed URLs and with artifactweave's own mirror;
- the Terraform conventions and tools for roots, child modules, mocked tests, linting,
  scanning and generated docs;
- how GitHub Actions authenticates to the three clouds without stored secrets;
- operational facts that shape the design: labels, secrets in state, reserved IDs, costs.

Decision [0030](decisions/0030-reference-deployments.md) records the design. CDN facts are
in [research 19](19-cdn-and-download-redirect.md).

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented by the vendor at the cited URL, read on 2026-10-14 |
| **[S]** | seen in source code, provider documentation in a source repository, or this repository |
| **[I]** | inferred from the documented facts; not stated by any source |

## 1. What exists today

| Item | Fact | Source |
|---|---|---|
| Infrastructure roots | Two: `deploy/terraform/acceptance` (GCP: bucket, service account, GitHub workload identity federation, lifecycle rules) and `deploy/terraform/acceptance-azure` (resource group, storage account with Shared Key disabled, container, user-assigned identity with a federated credential, lifecycle rules). Both keep local state. They now live at `deploy/platform/terraform/gcp/single-bucket` and `deploy/platform/terraform/azure/single-bucket` (#125) | [S] the two directories |
| Make targets | `gcp-%` and `azure-%` run a target in one fixed directory; paths inside each root climb `../../..` to the repository root | [S] `Makefile`, each root's `Makefile` and `compose.yaml` |
| App-layer Terraform | `test/terraform` applies the `jfrog/artifactory` provider against a running artifactweave; it provisions no infrastructure | [S] `test/terraform/main.tf`, `run.sh` |
| CI checks | Nothing formats, validates, lints, scans or tests the `deploy/` roots; Dependabot has no `terraform` ecosystem | [S] `.github/workflows/`, `.github/dependabot.yml` |
| GCP trust | The provider's `attribute_condition` checks the repository only; the `gcs` job in `cloud-conformance.yml` names no environment | [S] `deploy/terraform/acceptance/main.tf` (now `deploy/platform/terraform/gcp/single-bucket`), `.github/workflows/cloud-conformance.yml` |
| Azure trust | The federated credential matches `repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-conformance` exactly | [S] `deploy/terraform/acceptance-azure/variables.tf` (now `deploy/platform/terraform/azure/single-bucket`), `main.tf` |
| AWS | No Terraform and no real-AWS test; S3 is tested against an in-memory fake and MinIO | [S] `pkg/blob/s3/` |
| Repository | Public; owner ID 336817058, repository ID 1415179278, created 2026-10-12; `GET /repos/{owner}/{repo}/actions/oidc/customization/sub` reports `use_immutable_subject: true` with prefix `repo:weaveplatform@336817058/artifactweave@1415179278` | [S] `gh api`, 2026-10-14 |

## 2. Multi-region object storage

### 2.1 AWS

- **Cross-region replication** is asynchronous and needs versioning on both buckets
  [D] [s3-replication].
- **S3 Replication Time Control** "replicates most objects that you upload to Amazon S3 in
  seconds, and 99.9 percent of those objects within 15 minutes" [D] [s3-rtc]. It is
  enabled per replication rule, and the only valid value for `ReplicationTime` and
  `EventThreshold` is 15 minutes [D] [s3-rtc]. RTC rules publish replication metrics and
  `OperationMissedThreshold` events [D] [s3-rtc].
- **Multi-Region Access Points** route to the nearest bucket through a global hostname
  `<alias>.accesspoint.s3-global.amazonaws.com`. Requests "must be signed by using
  Signature Version 4A (SigV4A)" [D] [s3-mrap-requests].
- **CloudFront in front of a Multi-Region Access Point** needs an origin access control
  with signing protocol `sigv4a`. It is "not supported for MRAP endpoints backed by
  buckets in opt-in Regions", every underlying bucket needs its own policy, and Lambda@Edge
  origin triggers are incompatible with a SigV4a OAC [D] [cloudfront-mrap].
- **[I]** A presigned URL names one bucket. Failing over to a replica means signing against
  the replica, and an object written moments earlier can be missing there. A Multi-Region
  Access Point can route a read to a bucket that has not yet received the object.

### 2.2 Google Cloud

- **Dual-region and multi-region buckets** replicate "99.9% of newly written objects
  within a target of one hour" and "100% of newly written objects within a target of 12
  hours" by default [D] [gcs-availability].
- **Turbo replication** meets "the recovery point objective of 15 minutes" and "is only
  available for buckets in dual-regions" [D] [gcs-availability].
- **Consistency:** Cloud Storage is strongly consistent globally [D] [gcs-consistency].
  A dual- or multi-region bucket is one bucket, so one signed URL serves every location
  [I].

### 2.3 Azure

- **GRS and GZRS** copy asynchronously to the paired region. Data there "isn't available
  for read or write access unless there's a failover"; RA-GRS and RA-GZRS add read access
  [D] [azure-redundancy].
- **Secondary endpoint:** "The secondary endpoint appends the suffix *-secondary* to the
  account name", and "The account access keys for your storage account are the same for
  both the primary and secondary endpoints" [D] [azure-redundancy].
- **RPO:** geo priority replication "ensures the RPO for Block Blobs is less than or equal
  to 15 minutes" [D] [azure-redundancy].
- **Archive tier:** not supported on ZRS, GZRS or RA-GZRS accounts [D] [azure-redundancy].
- **Object replication** needs change feed on the source and blob versioning on both
  accounts. "After you create the replication policy, write operations to the destination
  container aren't permitted. Any attempts to write to the destination container fail with
  error code 409 (Conflict)." A source replicates to at most two destination accounts.
  Replication fails for blobs moved to the archive tier [D] [azure-object-replication].

### 2.4 Native replication and the mirror

- Native replicas are invisible to artifactweave's replica index and never count as copies
  [S] [0028 Decision](decisions/0028-blob-mirroring.md#decision).
- Research 18 recommends treating a dual-region bucket or an RA-GZRS account as one mirror
  member, and notes that an object-replication destination cannot be a writable member
  [S] [research 18 §3.2](18-cloud-blob-storage.md#32-cloud-native-replication).
- **[I]** So a deployment chooses one of two shapes: one store with the cloud's own
  replication, or several independent stores mirrored by artifactweave. Stacking both adds
  egress and, on Azure, blocks the mirror's writes.

## 3. Terraform practice

| Topic | Fact | Label and source |
|---|---|---|
| Child modules | "A module intended to be called by one or more other modules must not contain any provider blocks" | [D] [tf-module-providers] |
| Layout | "Nested modules should exist under the `modules/` subdirectory" | [D] [tf-module-structure] |
| Root size | Google advises keeping a root module's state under about 100 resources | [D] [gcp-root-modules] |
| Mocked tests | "Test mocking is available in Terraform v1.7.0 and later"; mocked providers work in `plan` and `apply` and generate computed values | [D] [tf-mocking] |
| Apply tests | A `run` block with `command = apply` creates real resources and Terraform tries to destroy them at the end; a failed destroy leaves them behind | [D] [tf-tests] |
| Write-only arguments | Need Terraform 1.11 or later and a resource that declares one; the value is never stored in state or plan files | [D] [tf-write-only] |
| tflint | Rulesets for AWS, Azure and Google are separate plugins declared in `.tflint.hcl` `plugin` blocks and installed with `tflint --init`; `--recursive` runs in each directory | [D] [tflint] |
| Scanning | tfsec now lives in Trivy (`trivy config`) | [D] [tfsec] |
| Supply chain | On 19 March 2026 an attacker rewrote 76 of 77 version tags of `aquasecurity/trivy-action` and all 7 of `aquasecurity/setup-trivy` to point at credential-stealing commits (CVE-2026-33634). Actions pinned by commit SHA were unaffected | [D] [cve-2026-33634] |
| terraform-docs | `--output-mode inject` replaces the content between `<!-- BEGIN_TF_DOCS -->` and `<!-- END_TF_DOCS -->` | [D] [terraform-docs-output] |
| terraform-docs check | A `--output-check` flag is listed in the CLI help; its exit status on a stale file is not documented on the configuration page | [I] [terraform-docs-output], secondary sources |
| S3 backend | Locking with `use_lockfile = true` writes a `.tflock` object beside the state; DynamoDB locking is deprecated | [D] [tf-s3-backend] |
| Variable validation | From Terraform 1.9, "Input variable validation rules can refer to other objects", so one variable's rule can check another (read 2026-10-15) | [D] [tf-changelog-1.9] |
| Module sources | "Add `//` to the source path to indicate that the rest of the path after that point is a sub-directory within the package"; query parameters such as `ref` follow the sub-directory, as in `git::https://example.com/network.git//modules/vpc?ref=v1.2.0` (read 2026-10-15) | [D] [tf-module-block] |
| Release-please extra files | The generic updater replaces the version on any line of a file listed in `extra-files` that carries the annotation `x-release-please-version`; a file can be forced to the generic updater with `{"type": "generic", "path": …}` (read 2026-10-15) | [D] [release-please-customizing] |

## 4. GitHub Actions identity

### 4.1 GitHub

- **Subject with an environment:** "The subject claim includes the environment name when
  the job references an environment", as `repo:ORG/REPO:environment:NAME` [D] [gh-oidc].
- **Immutable subject:** "repositories created after July 15, 2026 now use an immutable
  default subject format", which embeds IDs: `repo:OWNER@OWNER-ID/REPO@REPO-ID:...`
  [D] [gh-oidc]. This repository's subject for environment `X` is therefore
  `repo:weaveplatform@336817058/artifactweave@1415179278:environment:X` [S] (section 1,
  and the Azure credential that matches it).
- **One environment per job:** `jobs.<job_id>.environment` names the single environment a
  job references, as a name or a name and URL; all deployment protection rules must pass
  before the job is sent to a runner [D] [gh-workflow-syntax].
- **Required reviewers:** up to six users or teams, one approval needed. "Prevent
  self-reviews" is optional; when it is on, "users who initiate a deployment cannot
  approve the deployment job". On GitHub Free, Pro and Team, required reviewers are "only
  available for public repositories" [D] [gh-environments].
- **Deployment branches:** no restriction, protected branches only, or selected branches
  and tags [D] [gh-environments].

### 4.2 AWS

- An IAM OIDC provider for `https://token.actions.githubusercontent.com`. AWS verifies the
  JWKS endpoint's certificate against its own trusted root CAs and uses thumbprints only
  as a fallback [D] [aws-oidc-provider].
- The role trust policy matches `token.actions.githubusercontent.com:aud` =
  `sts.amazonaws.com` and `:sub` = the subject in section 4.1 [I] from [gh-oidc] and
  [aws-oidc-provider].

### 4.3 Google Cloud

- A workload identity pool and an OIDC provider with an attribute mapping and an attribute
  condition; Google requires an attribute condition for GitHub and recommends the numeric
  ID claims [D] [gcp-wif-pipelines].
- **Deleted pools:** "Soft-deleted pools are permanently deleted after approximately 30
  days ... You cannot reuse the ID of a soft-deleted pool until it is permanently deleted"
  [S] [tfgoogle-wif-pool].
- **[S]** The acceptance environment, including its `github-actions` pool, was destroyed on
  2026-10-14, so that ID is unavailable until about 2026-11-13 unless the pool is
  undeleted.

### 4.4 Azure

- A user-assigned identity with federated identity credentials. "A maximum of 20 federated
  identity credentials can be added to an application or user-assigned managed identity";
  "Wildcard characters aren't supported in any federated identity credential property
  value"; the subject must match exactly, and a mismatch fails without an error at creation
  [D] [azure-fic].
- Creating several credentials on one identity concurrently fails with 409; azurerm 3.40.0
  and later creates them in sequence [D] [azure-fic].

## 5. Operational facts

| Topic | Fact | Label and source |
|---|---|---|
| GCP labels | "Keys and values can contain only lowercase letters, numeric characters, underscores, and dashes"; values up to 63 characters. An ISO 8601 timestamp cannot be a value; epoch seconds can | [D] [gcp-labels]; [I] for the timestamp |
| GCP load balancing labels | Compute's labelling page lists label support for forwarding rules only on internal Application Load Balancers. In the Terraform provider, `google_compute_backend_bucket`, `google_compute_url_map` and `google_compute_target_https_proxy` have no `labels` argument | [D] [gcp-labels]; [S] [tfgoogle-docs] |
| Cloud CDN key in state | `google_compute_backend_bucket_signed_url_key.key_value` "will be stored in the raw state as plain text"; the resource has no write-only variant | [S] [tfgoogle-signed-url-key] |
| Generated keys in state | The managed `tls_private_key` resource stores the private key "*unencrypted* in your Terraform state file"; an ephemeral `tls_private_key` (Terraform 1.11) avoids state, but only feeds write-only arguments | [D] [tf-tls-private-key] |
| Front Door base fee | Standard $35 and Premium $330 per month, "Billed hourly and only for number of hours used"; "You're billed for each hour, or partial hour, that your profile is deployed" (read 2026-10-15); Private Link origins and managed WAF rule sets cost nothing extra on Premium | [D] [afd-pricing], [afd-billing] |
| Cloud load balancer | About $0.025 per hour for each of the first five forwarding rules | [I] planning research; the pricing page did not render for verification [gcp-lb-pricing] |
| Provisioning time | CloudFront distributions, Front Door profiles and Google-managed certificates take minutes to tens of minutes to create or delete | [I] |

## 6. Versions at the research baseline

| Component | Version | Note |
|---|---|---|
| Terraform | 1.16.4 in CI; 1.16.6 released 2026-10-14 | mocks need 1.7, write-only arguments 1.11 |
| `hashicorp/aws` | 6.68.0 (2026-10-07) | |
| `hashicorp/google` | 8.7.0 (2026-10-13); the acceptance root pins `~> 8.6` | |
| `hashicorp/azurerm` | 5.9.0 (2026-10-08) | |
| `Azure/azapi` | 2.13.0 (2026-09-28) | for operations azurerm lacks |
| tflint | 0.64.0 (2026-07-17) | rulesets: aws 0.49.0, google 0.40.0, azurerm 0.32.0 |
| Trivy | 0.76.0 (2026-10-14) | `trivy-action` 0.36.0; pin by commit SHA |
| terraform-docs | 0.24.0 (2026-05-10) | |
| `aws-actions/configure-aws-credentials` | 6.3.0 (2026-09-15) | |
| `google-github-actions/auth` | v3 | pinned as v3.0.0 in `cloud-conformance.yml` |
| `azure/login` | 3.1.0 (2026-09-10) | |

## Sources

**AWS**
- [s3-replication]: https://docs.aws.amazon.com/AmazonS3/latest/userguide/replication.html
- [s3-rtc]: https://docs.aws.amazon.com/AmazonS3/latest/userguide/replication-time-control.html
- [s3-mrap-requests]: https://docs.aws.amazon.com/AmazonS3/latest/userguide/MultiRegionAccessPointRequests.html
- [cloudfront-mrap]: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-restricting-access-to-s3-mrap.html
- [aws-oidc-provider]: https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_create_oidc.html

**Google Cloud**
- [gcs-availability]: https://docs.cloud.google.com/storage/docs/availability-durability
- [gcs-consistency]: https://docs.cloud.google.com/storage/docs/consistency
- [gcp-root-modules]: https://docs.cloud.google.com/docs/terraform/best-practices/root-modules
- [gcp-wif-pipelines]: https://docs.cloud.google.com/iam/docs/workload-identity-federation-with-deployment-pipelines
- [gcp-labels]: https://docs.cloud.google.com/compute/docs/labeling-resources
- [gcp-lb-pricing]: https://cloud.google.com/load-balancing/pricing
- [tfgoogle-docs]: https://github.com/hashicorp/terraform-provider-google/tree/main/website/docs/r (`compute_backend_bucket`, `compute_url_map`, `compute_target_https_proxy`, `compute_global_forwarding_rule`)
- [tfgoogle-signed-url-key]: https://github.com/hashicorp/terraform-provider-google/blob/main/website/docs/r/compute_backend_bucket_signed_url_key.html.markdown
- [tfgoogle-wif-pool]: https://github.com/hashicorp/terraform-provider-google/blob/main/website/docs/r/iam_workload_identity_pool.html.markdown

**Azure**
- [azure-redundancy]: https://learn.microsoft.com/en-us/azure/storage/common/storage-redundancy
- [azure-object-replication]: https://learn.microsoft.com/en-us/azure/storage/blobs/object-replication-overview
- [azure-fic]: https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation-considerations
- [afd-pricing]: https://azure.microsoft.com/en-us/pricing/details/frontdoor/
- [afd-billing]: https://learn.microsoft.com/en-us/azure/frontdoor/billing

**Terraform and tools**
- [tf-module-providers]: https://developer.hashicorp.com/terraform/language/modules/develop/providers
- [tf-module-structure]: https://developer.hashicorp.com/terraform/language/modules/develop/structure
- [tf-tests]: https://developer.hashicorp.com/terraform/language/tests
- [tf-mocking]: https://developer.hashicorp.com/terraform/language/tests/mocking
- [tf-write-only]: https://developer.hashicorp.com/terraform/language/manage-sensitive-data/write-only
- [tf-tls-private-key]: https://github.com/hashicorp/terraform-provider-tls/blob/main/docs/resources/private_key.md
- [tf-s3-backend]: https://developer.hashicorp.com/terraform/language/backend/s3
- [tflint]: https://github.com/terraform-linters/tflint
- [tfsec]: https://github.com/aquasecurity/tfsec
- [cve-2026-33634]: https://cve.circl.lu/cve/CVE-2026-33634
- [terraform-docs-output]: https://terraform-docs.io/user-guide/configuration/output/
- [tf-changelog-1.9]: https://github.com/hashicorp/terraform/blob/v1.9/CHANGELOG.md
- [tf-module-block]: https://developer.hashicorp.com/terraform/language/block/module
- [release-please-customizing]: https://github.com/googleapis/release-please/blob/main/docs/customizing.md

**GitHub**
- [gh-oidc]: https://docs.github.com/en/actions/reference/security/oidc
- [gh-workflow-syntax]: https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax
- [gh-environments]: https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments
