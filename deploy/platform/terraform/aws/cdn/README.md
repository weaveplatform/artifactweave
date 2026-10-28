# aws/cdn: an S3 store whose downloads CloudFront serves

A private S3 bucket behind a CloudFront distribution. artifactweave writes to the bucket and
answers a download from a `downloadRedirect` or `cdnRedirect` repository with a 302 to a
CloudFront URL it signs, so the bytes come from the edge nearest the client rather than from
artifactweave or the bucket's region. Choose it when clients are spread across regions or
download the same artifacts often. Choose `single-bucket` when they sit beside the bucket, or
`multi-region` for copies in several regions that artifactweave writes itself.

## What it deploys

- The bucket through [`modules/aws/storage`](../../modules/aws/storage/README.md),
  `<name>-<suffix>` in `region`. Its policy grants `app_role_arn` the bucket's objects and, with
  the CDN module's statement, lets the distribution read the committed blobs.
- The distribution through [`modules/aws/cdn`](../../modules/aws/cdn/README.md): origin access
  control to the bucket, a cache policy keyed on the path and the two response overrides, a key
  group holding `cloudfront_public_key`, and the distribution itself. With `domain_name`, also an
  ACM certificate in `us-east-1`, its validation record and A and AAAA alias records in
  `zone_id`.
- `random_id.suffix`: eight hex characters shared by the names, so the bucket is globally
  unique.

## How it behaves

- **Uploads go to the bucket**, as with `single-bucket`. CloudFront only reads.
- **Downloads redirect to the edge.** A GET of a file of at least 1 MiB
  (`download.redirect.min_size`) from a repository with `downloadRedirect` or `cdnRedirect`
  answers 302 to a CloudFront URL on `storage_host`, signed with a canned policy (decision
  [0029](../../../../../docs/research/decisions/0029-cdn-delivery-and-download-redirect.md)).
  Smaller files, and HEAD and conditional requests, are answered by artifactweave itself, so the
  JFrog CLI's HEAD before a split download works. Each ranged GET of a split download is
  redirected too, and CloudFront serves the range.
- **The redirect header still works.** `X-JFrog-Download-Redirect-To: None` keeps a download on
  artifactweave, and `S3` sends a `downloadRedirect` repository's download to a presigned URL on
  the bucket itself, `origin_host`.
- **CloudFront checks the signature on every request**, cached or not, so a URL works until it
  expires and not after: at the end of the current window plus `download.redirect.ttl`
  (5 minutes by default), so every client in one window gets the same URL and the edge cache
  hits. A request without a signature, or with an expired or altered one, gets 403.
- **The edge caches by path and filename.** The cache key is the object key plus the
  `response-content-disposition` and `response-content-type` overrides artifactweave signs into
  the URL, so two items with the same content each keep their own filename. Blobs never change,
  so an edge copy lives up to a day by default. Deleting an item does not purge the edge, but
  a URL issued before the delete stops working when it expires.
- **Clients drop their credentials on the redirect** because the CDN host is never
  artifactweave's host or a subdomain of it; artifactweave refuses to start otherwise. With
  `domain_name`, choose a sibling of artifactweave's host, such as `cdn.example.com` beside
  `artifacts.example.com`.

## Prerequisites

- Terraform 1.9 or later, and AWS credentials the SDK chain finds (`aws login`, a profile, a
  CI role) for the account that will hold the bucket and the distribution.
- Rights to create an S3 bucket and its policy, encryption, lifecycle and public access
  settings, and CloudFront origin access controls, cache policies, public keys, key groups and
  distributions. With `domain_name`, also ACM certificates in `us-east-1` and records in the
  Route 53 zone `zone_id`, which must be a public zone that resolves.
- The IAM role artifactweave runs as, created elsewhere (an EC2 instance profile's role, an
  EKS Pod Identity role). This scenario grants it the bucket's objects; it does not create it.
- A CloudFront key pair you generate yourself, so the private half never reaches Terraform:

  ```bash
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out cloudfront-private-key.pem
  openssl pkey -in cloudfront-private-key.pem -pubout -out cloudfront-public-key.pem
  ```

### Getting the code

Copy this directory together with `modules/aws/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point each module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/storage?ref=v0.2.0" # x-release-please-version
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/cdn?ref=v0.2.0" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
name         = "artifactweave-example"
region       = "eu-west-2"
app_role_arn = "arn:aws:iam::111122223333:role/artifactweave"

# Optional
# cloudfront_private_key_file = "/etc/artifactweave/cloudfront-private-key.pem"
# domain_name                 = "cdn.example.com"
# zone_id                     = "Z0123456789EXAMPLE"
# price_class                 = "PriceClass_100"
# store_prefix                = "artifacts/"
# expiring_prefixes           = { "scratch/" = 1 }
# force_destroy               = true
# tags                        = { team = "platform" }
```

The public key is a PEM file, which a `.tfvars` file cannot read, so pass it from the
environment:

```bash
export TF_VAR_cloudfront_public_key="$(cat cloudfront-public-key.pem)"
terraform init
terraform apply
```

CloudFront takes several minutes to deploy a new distribution, and `terraform apply` waits for
it.

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `s3` store, the
bucket, in its region, with `prefix` only when `store_prefix` is set, and a `cdn.cloudfront`
block naming the CDN host, the public key's ID and where the private key is. The store names
no credentials: the AWS SDK chain supplies the app role's. The example below is the rendered
output for the example inputs above, with the random suffix `0a1b2c3d`; the `readme_example`
test fails if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: s3
  s3:
    bucket: artifactweave-example-0a1b2c3d
    region: eu-west-2
    cdn:
      cloudfront:
        domain: d111111abcdef8.cloudfront.net
        key_pair_id: K2JCJMDEHXQW5F
        private_key_file: /etc/artifactweave/cloudfront-private-key.pem
```
<!-- END_CONFIG_EXAMPLE -->

### Running artifactweave with it

```bash
terraform output -raw artifactweave_config > artifactweave.yaml
sudo install -m 0600 -o artifactweave cloudfront-private-key.pem /etc/artifactweave/cloudfront-private-key.pem
export ARTIFACTWEAVE_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: it seals stored secrets
export ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD='choose one'      # read on first start only
artifactweave serve --config artifactweave.yaml --base-url https://artifacts.example.com
```

Run it as `app_role_arn`, on an instance or pod that has the role, so the AWS SDK chain finds
its credentials, with the private key at `cloudfront_private_key_file`: artifactweave reads it
at start. Then turn on `downloadRedirect` or `cdnRedirect` on the repositories whose downloads
the edge should serve; [`deploy/artifactweave/terraform/cdn-repositories`](../../../../artifactweave/terraform/cdn-repositories/README.md)
does it with the JFrog provider. Their downloads redirect to `storage_host`. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key, `download.redirect.ttl` and `download.redirect.min_size` among them.

## Security

The bucket carries the [`modules/aws/storage` invariants](../../modules/aws/storage/README.md#invariants):
public access blocked, ACLs disabled, TLS required, S3-managed encryption and abandoned uploads
expired. The distribution carries the
[`modules/aws/cdn` invariants](../../modules/aws/cdn/README.md#invariants): it reads the bucket
through origin access control and may read the committed blobs and nothing else, it serves
only URLs signed by a key in its key group, over HTTPS, and a host of your own gets TLS 1.2 at
least.

The app role can read, write, list and delete objects in the bucket through its policy, and
nothing else. The private key lets whoever holds it mint URLs for any blob in the bucket until
the key is removed from the key group, so keep it readable by artifactweave alone. To rotate
it, use [`modules/aws/cdn`](../../modules/aws/cdn/README.md) directly with both public keys,
point artifactweave at the new private key, and remove the old public key once the URLs it
signed have expired. Terraform's state holds the public key only.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AWS-0089 | `aws_s3_bucket.blobs` (`aws/storage`) | No access logging: CloudTrail records the management calls, and server access logs need a second bucket. |
| AWS-0090 | `aws_s3_bucket.blobs` (`aws/storage`) | No versioning: blobs are content-addressed and never rewritten, and versioning would keep every deleted upload part. |
| AWS-0132 | `aws_s3_bucket_server_side_encryption_configuration.blobs` (`aws/storage`) | S3-managed keys; a KMS key would add a monthly fee, and CloudFront would need a grant on it. |
| AWS-0010 | `aws_cloudfront_distribution.cdn` (`aws/cdn`) | No access logs: standard logging needs a bucket with ACLs on, which the storage baseline turns off, and artifactweave counts every download it redirects. |
| AWS-0011 | `aws_cloudfront_distribution.cdn` (`aws/cdn`) | No WAF: CloudFront refuses every request without a valid signature before it reaches the bucket, and a web ACL is billed monthly. |

## Cost

### Steady state

Storage is S3 Standard, roughly $0.023 to $0.024 per GB-month in the European regions, plus
requests. CloudFront bills data transferred to viewers (roughly $0.085 per GB for the first
10 TB in North America and Europe, more elsewhere unless `price_class` leaves those locations
out) and HTTPS requests (roughly $0.01 per 10,000); its always-free tier covers the first 1 TB
and 10 million requests each month. Transfer from the bucket to CloudFront is free. ACM's public
certificate is free, and Route 53 alias queries to CloudFront are free. There is no fixed or
hourly charge.

### Per test run

About one cent: a proof stores well under 100 MiB for minutes and downloads under 200 MiB
through CloudFront and the bucket, all within CloudFront's free tier on a lab account. Its
duration is mostly CloudFront deploying and deleting the distribution, several minutes each.

## Teardown

`terraform destroy` disables the distribution, waits until every edge location has the change
(commonly 5 to 15 minutes), then deletes it, with the key group, public key, cache policy,
origin access control, records and certificate. It fails while the bucket holds any object
unless `force_destroy` is true, which deletes them with the bucket. Nothing outlives the
scenario but the private key on your hosts, which no distribution trusts any more; the app
role and the zone are not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the composition:
  `readme_example` renders the example above from the example inputs; `ci_run` uses the longest
  instance a proof produces, with its own host in a zone, checking that the bucket and
  CloudFront names fit their limits and start with the instance's name, and the outputs reap
  and `test/platform` read; others check that the bucket's policy carries the distribution's
  read grant, that the distribution fronts this bucket, and the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, a module it calls or the scenario driver) applies an instance as the CI deployer,
  which also runs artifactweave, with a host of its own, `cdn-<instance>` in the bootstrap's
  zone, and runs `test/platform`. The generic checks' redirected downloads, whole and ranged,
  go through CloudFront, and a URL without its signature gets 403.
  [`cdn_test.go`](../../../../../test/platform/cdn_test.go) adds: a `cdnRedirect` repository
  redirects to the CDN host; `X-JFrog-Download-Redirect-To: S3` lands on the bucket; two items
  with the same content keep their own filenames through the edge cache; a URL signed with the
  right key but already expired gets 403; and `jf rt dl` splits a 40 MiB download across the
  edge. The proof then destroys the instance, applies and destroys another from its remote
  state alone, and checks that reap removes an expired one.
- **Harness outputs** (`harness.tf`): `expires_at`, the tag reap reads from the state.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `name` | `${NAME_PREFIX}${INSTANCE}`: `artifactweave-awscn-<instance>`, inside the bucket names the deployer may manage | `artifactweave-cdn` |
| `region` | `PLATFORM_AWS_REGION` | `LAB_AWS_REGION` |
| `app_role_arn` | `PLATFORM_AWS_ROLE_ARN`, the deployer | the bootstrap's conformance role, in the signed-in account |
| `cloudfront_public_key` | `CLOUDFRONT_PUBLIC_KEY`: the public half of a key pair `scenario-ci.sh` generates for the instance | the public half of a key pair `lab-vars.sh` generates once, in `.secrets/cloudfront/` |
| `cloudfront_private_key_file` | `CLOUDFRONT_PRIVATE_KEY_FILE`, in the run's temporary directory | `.secrets/cloudfront/cloudfront-private-key.pem` in this directory |
| `zone_id` | `PLATFORM_AWS_DNS_ZONE_ID`, the bootstrap's zone | `LAB_AWS_ZONE_ID` |
| `domain_name` | `cdn-<instance>.<zone>`, the zone's name looked up by `scenario-ci.sh` | `cdn.<LAB_AWS_ZONE_NAME>` |
| `tags` | `$SCENARIO_LABELS`: `artifactweave-scenario`, `instance`, `expires-at` | none |
| `force_destroy` | `true` | `true` |

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| aws | ~> 6.67 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| random | 3.9.1 |

### Modules

| Name | Source | Version |
| ---- | ------ | ------- |
| cdn | ../../modules/aws/cdn | n/a |
| storage | ../../modules/aws/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_role\_arn | ARN of the IAM role artifactweave runs as (its instance profile's or workload's role). The bucket policy grants it the bucket's objects. | `string` | n/a | yes |
| cloudfront\_private\_key\_file | Where the private half of the key pair is on the host artifactweave runs on (blob.s3.cdn.cloudfront.private\_key\_file). Terraform never reads it; the configuration names it. | `string` | `"/etc/artifactweave/cloudfront-private-key.pem"` | no |
| cloudfront\_public\_key | The PEM public key (RSA 2048 or ECDSA P-256) of the key pair artifactweave signs CloudFront URLs with. Generate the pair yourself and keep the private half out of Terraform: the state holds this value. | `string` | n/a | yes |
| domain\_name | A host of your own for the CDN, such as cdn.example.com, a sibling of artifactweave's host and never a subdomain of it. Null serves from the distribution's *.cloudfront.net host. | `string` | `null` | no |
| expiring\_prefixes | Key prefixes, from the bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails while the bucket holds anything. | `bool` | `false` | no |
| name | Start of every name: the bucket and the CloudFront origin access control, cache policy and key group are <name>-<8 hex>, within S3's 63 characters. | `string` | n/a | yes |
| price\_class | Which CloudFront edge locations serve the blobs: PriceClass\_All, PriceClass\_200 or PriceClass\_100. | `string` | `"PriceClass_All"` | no |
| region | Region of the bucket, where artifactweave writes (blob.s3.region). CloudFront itself is global. | `string` | n/a | yes |
| store\_prefix | Key prefix artifactweave writes under (blob.s3.prefix). CloudFront may read only the committed blobs under it. | `string` | `""` | no |
| tags | Tags for the bucket, the distribution and the certificate. | `map(string)` | `{}` | no |
| zone\_id | ID of the Route 53 public hosted zone holding domain\_name, for its certificate's validation record and alias records. Set it with domain\_name, and only then. | `string` | `null` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml: one s3 store, the bucket, with CloudFront in front of it. It authenticates through the AWS SDK chain, as the app role, and signs CloudFront URLs with the private key at cloudfront\_private\_key\_file. |
| bucket | The bucket, for blob.s3.bucket. |
| distribution\_domain | The distribution's own *.cloudfront.net host, which domain\_name's alias records point at. |
| distribution\_id | The CloudFront distribution's ID, for the console and CLI. |
| expires\_at | For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| origin\_host | The bucket's virtual-hosted endpoint, where a downloadRedirect repository's presigned URLs point when a client sends X-JFrog-Download-Redirect-To: S3. |
| region | The bucket's region, for blob.s3.region. |
| storage\_host | Where a downloadRedirect or cdnRedirect repository's signed URLs point: the CDN host, domain\_name or the distribution's *.cloudfront.net host. |
<!-- END_TF_DOCS -->
