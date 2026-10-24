# gcp/cdn: a Cloud Storage store whose downloads Cloud CDN serves

A private Cloud Storage bucket behind Cloud CDN, on a global external Application Load
Balancer with a host of your own. artifactweave writes to the bucket and answers a download
from a `downloadRedirect` or `cdnRedirect` repository with a 302 to a Cloud CDN URL it signs,
so the bytes come from the edge nearest the client rather than from artifactweave or the
bucket's region. Choose it when clients are spread across regions or download the same
artifacts often. Choose `single-bucket` when they sit beside the bucket, `geo-replicated` for
one bucket in two regions, or `multi-region` for copies in several regions that artifactweave
writes itself.

## What it deploys

- The bucket through [`modules/gcp/storage`](../../modules/gcp/storage/README.md),
  `<name>-<suffix>` in `location`, with object rights for `app_service_account`.
- Cloud CDN through [`modules/gcp/cdn`](../../modules/gcp/cdn/README.md): a backend bucket
  with Cloud CDN on, its URL map, an SSL policy, a Google-managed certificate for
  `domain_name` from Certificate Manager with its DNS authorisation and certificate map, the
  HTTPS proxy, a global address and an HTTPS forwarding rule. With `dns_zone`, also the
  authorisation record and the host's A record in that zone. With `signed_url_key_added`, read
  access to the committed blobs for Cloud CDN's fill account.
- `random_id.suffix`: eight hex characters shared by the names, so the bucket is globally
  unique.

## How it behaves

- **Uploads go to the bucket**, as with `single-bucket`. Cloud CDN only reads.
- **Downloads redirect to the edge.** A GET of a file of at least 1 MiB
  (`download.redirect.min_size`) from a repository with `downloadRedirect` or `cdnRedirect`
  answers 302 to a Cloud CDN URL on `storage_host`, signed with the key named
  `signed_url_key_name` (decision
  [0029](../../../../../docs/research/decisions/0029-cdn-delivery-and-download-redirect.md)).
  Smaller files, and HEAD and conditional requests, are answered by artifactweave itself, so the
  JFrog CLI's HEAD before a split download works. Each ranged GET of a split download is
  redirected too, and Cloud CDN serves the range.
- **The redirect header still works.** `X-JFrog-Download-Redirect-To: None` keeps a download on
  artifactweave, and `S3` sends a `downloadRedirect` repository's download to a signed URL on
  Cloud Storage itself, `origin_host`, signed by `app_service_account`.
- **Cloud CDN checks the signature on every request**, cached or not, so a URL works until it
  expires and not after: at the end of the current window plus `download.redirect.ttl`
  (5 minutes by default), so every client in one window gets the same URL and the edge cache
  hits. A request with an expired or altered signature gets 403.
- **The bucket refuses what Cloud CDN does not.** Cloud CDN forwards a request without a
  signature to the bucket instead of refusing it, and caches signed and unsigned responses
  apart; the bucket is private and admits only the fill account, so the request gets 403.
- **The edge caches by path and filename.** The cache key holds the
  `response-content-disposition` and `response-content-type` overrides artifactweave signs into
  the URL, so two items with the same content each keep their own filename. A signed response
  stays fresh for a day; blobs never change. Deleting an item does not purge the edge, but a
  URL issued before the delete stops working when it expires.
- **Clients drop their credentials on the redirect** because `domain_name` is never
  artifactweave's host or a subdomain of it; artifactweave refuses to start otherwise. Choose
  a sibling of artifactweave's host, such as `cdn.example.com` beside
  `artifacts.example.com`.

## Prerequisites

- Terraform 1.9 or later, the gcloud CLI, and Application Default Credentials (`gcloud auth
  application-default login`) for a principal that can, in `project_id`, create buckets and
  set their IAM policy (Storage Admin), load balancers and signed URL keys (Compute Load
  Balancer Admin), and Certificate Manager certificates (Certificate Manager Editor). The
  Cloud Storage, Compute Engine, Certificate Manager and, with `dns_zone`, Cloud DNS APIs must
  be enabled.
- With `dns_zone`, a public Cloud DNS zone in `project_id` that resolves and holds
  `domain_name`, and DNS Administrator on it. Without it, rights to add two records wherever
  the domain is served.
- The service account artifactweave runs as, created elsewhere (a workload identity's, or a
  VM's). This scenario grants it object rights on the bucket; it does not create it. It signs
  storage URLs through the IAM Credentials API, so it needs
  `roles/iam.serviceAccountTokenCreator` on itself.
- A signed URL key you generate yourself, so it never reaches Terraform: 16 random bytes,
  base64url.

  ```bash
  head -c 16 /dev/urandom | base64 | tr +/ -_ > cloud-cdn-key
  ```

### Getting the code

Copy this directory together with `modules/gcp/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point each module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/gcp/storage?ref=v0.1.1" # x-release-please-version
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/gcp/cdn?ref=v0.1.1" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
project_id          = "example-project"
name                = "artifactweave-example"
location            = "europe-west2"
app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"
domain_name         = "cdn.example.com"
dns_zone            = "example-com"

# Optional
# signed_url_key_name = "artifactweave"
# cloud_cdn_key_file  = "/etc/artifactweave/cloud-cdn-key"
# store_prefix        = "artifacts/"
# expiring_prefixes   = { "scratch/" = 1 }
# force_destroy       = true
# labels              = { team = "platform" }
```

The key never reaches Terraform, and Google creates Cloud CDN's fill account only when a
project's first key is added, so a new deployment takes three steps:

```bash
terraform init
terraform apply                                   # the bucket, the load balancer, the certificate
gcloud compute backend-buckets add-signed-url-key "$(terraform output -raw backend_bucket)" \
  --key-name artifactweave --key-file cloud-cdn-key --project example-project
terraform apply -var signed_url_key_added=true    # the fill account's read access
```

Set `signed_url_key_added = true` in `terraform.tfvars` once the key is added, so later applies
keep the grant. Google issues the certificate once the authorisation record resolves, and the
load balancer needs a few minutes more to answer on the new address: allow up to an hour
before the first download through the edge succeeds.

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `gcs` store, the
bucket, with `prefix` only when `store_prefix` is set, the app service account as
`google_access_id` to sign storage URLs, and a `cdn.cloud_cdn` block naming the CDN host, the
key's name and where the key is. The store names no credentials: Application Default
Credentials supply the app service account's. The example below is the rendered output for
the example inputs above, with the random suffix `0a1b2c3d`; the `readme_example` test fails
if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: gcs
  gcs:
    bucket: artifactweave-example-0a1b2c3d
    google_access_id: artifactweave@example-project.iam.gserviceaccount.com
    cdn:
      cloud_cdn:
        domain: cdn.example.com
        key_name: artifactweave
        key_file: /etc/artifactweave/cloud-cdn-key
```
<!-- END_CONFIG_EXAMPLE -->

### Running artifactweave with it

```bash
terraform output -raw artifactweave_config > artifactweave.yaml
sudo install -m 0600 -o artifactweave cloud-cdn-key /etc/artifactweave/cloud-cdn-key
export ARTIFACTWEAVE_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: it seals stored secrets
export ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD='choose one'      # read on first start only
artifactweave serve --config artifactweave.yaml --base-url https://artifacts.example.com
```

Run it as `app_service_account`, through workload identity or a VM's service account, so
Application Default Credentials find it, with the key at `cloud_cdn_key_file`: artifactweave
reads it at start. Then turn on `downloadRedirect` or `cdnRedirect` on the repositories whose
downloads the edge should serve;
[`deploy/artifactweave/terraform/cdn-repositories`](../../../../artifactweave/terraform/cdn-repositories/README.md)
does it with the JFrog provider. Their downloads redirect to `storage_host`. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key, `download.redirect.ttl` and `download.redirect.min_size` among them.

## Security

The bucket carries the [`modules/gcp/storage` invariants](../../modules/gcp/storage/README.md#invariants):
public access prevented, uniform bucket-level access, soft delete off and abandoned uploads
expired. The CDN carries the [`modules/gcp/cdn` invariants](../../modules/gcp/cdn/README.md#invariants):
it serves HTTPS only, with TLS 1.2 at least, from a Google-managed certificate; Cloud CDN's
fill account may read the committed blobs and nothing else, and no one else is granted the
bucket, so a request without a valid signature never gets a blob.

The app service account can read, write, list and delete objects in the bucket, and nothing
else. The key lets whoever holds it mint URLs for any blob in the bucket until it is removed
from the backend bucket, so keep it readable by artifactweave alone. To rotate it, add a second
key under a new name (a backend bucket holds up to three), point artifactweave at it, and
remove the old one with `gcloud compute backend-buckets delete-signed-url-key` once the URLs it
signed have expired. Terraform's state holds the key's name only.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| GCP-0066 | `google_storage_bucket.blobs` (`gcp/storage`) | Google-managed encryption keys; a customer-managed key would add a KMS key ring and its grants to manage. |
| GCP-0077 | `google_storage_bucket.blobs` (`gcp/storage`) | No access-log bucket: Cloud Audit Logs record the administrative calls, and object access logs need a second bucket. |
| GCP-0078 | `google_storage_bucket.blobs` (`gcp/storage`) | No versioning: blobs are content-addressed and never rewritten, and versioning would keep every deleted upload part. |
| GCP-0007 | `google_storage_bucket_iam_member.app` (`gcp/storage`) | Object admin for a service account, on this one bucket rather than the project; the backend deletes the temporary parts it composes. |

## Cost

### Steady state

Storage is Standard in one region (roughly $0.020 to $0.023 per GB-month in the European
regions), plus operations. The forwarding rule bills by the hour, roughly $0.025 an hour for a
project's first five, about $18 a month, whether or not anything is downloaded, and the
load balancer bills the data it processes. Cloud CDN bills cache egress to clients (roughly
$0.02 to $0.20 per GiB by region), cache fill from the bucket (roughly $0.01 to $0.04 per GiB)
and lookups (roughly $0.0075 per 10,000). The certificate and its map, the address while the
forwarding rule uses it, and the DNS records cost little or nothing beside these.

### Per test run

A few cents: a proof keeps up to three instances' forwarding rules for well under two hours
in all, stores well under 100 MiB for minutes and downloads under 200 MiB through Cloud CDN and
Cloud Storage. Its duration is mostly Google issuing the certificate and the load balancer
coming up, commonly 10 to 30 minutes.

## Teardown

`terraform destroy` removes the forwarding rule, the proxy, the URL map, the backend bucket
with its signed URL keys, the SSL policy, the address, the certificate, its map and
authorisation, the records and the fill account's grant. It fails while the bucket holds any
object unless `force_destroy` is true, which deletes them with the bucket. Nothing outlives
the scenario but the key on your hosts, which no backend bucket trusts any more, and Cloud
CDN's fill account, which Google keeps in the project; the app service account and the zone
are not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the composition:
  `readme_example` renders the example above from the example inputs; `ci_run` uses the longest
  instance a proof produces, with its own host in a zone and the key added, checking that the
  names fit their limits and start with the instance's name, that the fill account is granted,
  and the outputs reap and `test/platform` read; others check that the CDN fronts this bucket,
  that nothing is granted until the key is added, that the records come back without a zone,
  and the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, a module it calls or the scenario driver) applies an instance as the CI deployer,
  which also runs artifactweave, with a host of its own, `cdn-<instance>` in the bootstrap's
  zone, adds a signed URL key the job generates with `deploy/ci/cloud-cdn-key.sh`, and applies
  again with `signed_url_key_added`. `test/platform` first deploys a blob of its own and
  waits, up to 45 minutes, until the edge serves it from the URL artifactweave redirects to,
  which takes the certificate, the load balancer and the grant. The generic checks'
  redirected downloads, whole and ranged, then go through Cloud CDN, and a URL without its
  signature gets 403.
  [`cdn_test.go`](../../../../../test/platform/cdn_test.go) adds: a `cdnRedirect` repository
  redirects to the CDN host; `X-JFrog-Download-Redirect-To: S3` lands on Cloud Storage; Cloud
  CDN passes both response overrides to the bucket on a cache fill; two items with the same
  content keep their own filenames through the edge cache; a URL signed with the right key but
  already expired gets 403; and `jf rt dl` splits a 40 MiB download across the edge. The proof
  then destroys the instance, applies and destroys another from its remote state alone, and
  checks that reap removes an expired one.
- **Harness outputs** (`harness.tf`): `expires_at`, the label reap reads from the state, and
  `signed_url_key_name`, the key's name, which `scenario-ci.sh` and `lab-vars.sh` hand to
  `deploy/ci/cloud-cdn-key.sh`.
- **Locally**, `make apply`, `signed-url-key`, `apply` again, then `up`, `smoke`, `verify` and
  `destroy` run the same checks against an instance in the lab
  ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `project_id` | `PLATFORM_GCP_PROJECT` | `LAB_GCP_PROJECT` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `awscn-<instance>`, the start reap knows the load balancer's pieces by | `artifactweave-cdn` |
| `location` | `europe-west2` | `europe-west2` |
| `app_service_account` | `PLATFORM_GCP_SERVICE_ACCOUNT`, the deployer | the bootstrap's conformance service account |
| `cloud_cdn_key_file` | `CLOUD_CDN_KEY_FILE`: a key `scenario-ci.sh` generates for the instance, in the run's temporary directory | `.secrets/cloud-cdn/cloud-cdn-key` in this directory, generated once by `lab-vars.sh` |
| `dns_zone` | `PLATFORM_GCP_DNS_ZONE`, the bootstrap's zone | `LAB_GCP_DNS_ZONE` |
| `domain_name` | `cdn-<instance>.<zone>`, the zone's name looked up by `scenario-ci.sh` | `cdn.<LAB_GCP_DNS_NAME>` |
| `signed_url_key_added` | `true` on the second apply, once `scenario-ci.sh` has added the key | `true` once `make signed-url-key` has added the key to the backend bucket |
| `labels` | `$SCENARIO_LABELS`: `artifactweave-scenario`, `instance`, `expires-at` | none |
| `force_destroy` | `true` | `true` |

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| google | ~> 8.6 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| random | 3.9.1 |

### Modules

| Name | Source | Version |
| ---- | ------ | ------- |
| cdn | ../../modules/gcp/cdn | n/a |
| storage | ../../modules/gcp/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_service\_account | Email of the service account artifactweave runs as. It gets object rights on the bucket, and signs the storage URLs X-JFrog-Download-Redirect-To: S3 asks for (blob.gcs.google\_access\_id). | `string` | n/a | yes |
| cloud\_cdn\_key\_file | Where the signed URL key is on the host artifactweave runs on (blob.gcs.cdn.cloud\_cdn.key\_file): 16 random bytes, base64url. Terraform never reads it; the configuration names it. | `string` | `"/etc/artifactweave/cloud-cdn-key"` | no |
| dns\_zone | Name of the Cloud DNS managed zone in project\_id that holds domain\_name, where the certificate's authorisation record and the host's A record go. Null creates no records: add the two in the dns\_records output where the domain is served. | `string` | `null` | no |
| domain\_name | The CDN's host, such as cdn.example.com, a sibling of artifactweave's host and never a subdomain of it, served with a Google-managed certificate. | `string` | n/a | yes |
| expiring\_prefixes | Object prefixes, from the bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails while the bucket holds anything. | `bool` | `false` | no |
| labels | Labels for the bucket, the load balancer's address and forwarding rule, and the certificate's pieces. | `map(string)` | `{}` | no |
| location | Region of the bucket, where artifactweave writes, for example europe-west2. The load balancer and Cloud CDN are global. | `string` | n/a | yes |
| name | Start of every name: the bucket, the load balancer's pieces and the certificate's are <name>-<8 hex>, within the 63 characters Cloud Storage and Compute Engine allow. | `string` | n/a | yes |
| project\_id | Project that holds the bucket, the load balancer and the certificate. Nothing is created anywhere else. | `string` | n/a | yes |
| signed\_url\_key\_added | Whether the signed URL key has been added to the backend bucket. Set it once you have added the key: it lets Cloud CDN's fill account read the blobs, an account Google creates with the project's first key. Until then Cloud CDN serves nothing. | `bool` | `false` | no |
| signed\_url\_key\_name | Name of the signed URL key artifactweave signs with (blob.gcs.cdn.cloud\_cdn.key\_name), as given to gcloud compute backend-buckets add-signed-url-key. | `string` | `"artifactweave"` | no |
| store\_prefix | Object prefix artifactweave writes under (blob.gcs.prefix). Cloud CDN may read only the committed blobs under it. | `string` | `""` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml: one gcs store, the bucket, with Cloud CDN in front of it. It authenticates with Application Default Credentials, as the app service account, which also signs storage URLs, and signs Cloud CDN URLs with the key at cloud\_cdn\_key\_file. |
| backend\_bucket | The backend bucket's name, which gcloud compute backend-buckets add-signed-url-key takes. |
| bucket | The bucket's name (blob.gcs.bucket). |
| dns\_records | The records domain\_name needs, as name, type and data: the certificate's DNS authorisation and the host's A record. With dns\_zone they are created there; without it, add them where the domain is served. |
| expires\_at | For the test harness: the expires-at label (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| ip\_address | The load balancer's address, which domain\_name's A record names. |
| origin\_host | Cloud Storage's path-style endpoint, where a downloadRedirect repository's signed URLs point when a client sends X-JFrog-Download-Redirect-To: S3. |
| signed\_url\_key\_name | For the test harness: the signed URL key's name, which scenario-ci.sh and lab-vars.sh hand to cloud-cdn-key.sh to add the key after the first apply. |
| storage\_host | Where a downloadRedirect or cdnRedirect repository's signed URLs point: the CDN host, domain\_name. |
<!-- END_TF_DOCS -->
