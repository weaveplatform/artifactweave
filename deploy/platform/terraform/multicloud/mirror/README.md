# multicloud/mirror: one mirror across S3, Cloud Storage and Azure Blob

An S3 bucket, a Cloud Storage bucket and an Azure storage account as the three members of one
artifactweave mirror (decision
[0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Every blob is written to
each provider, an upload succeeds once `write_quorum` of them hold it (two by default), and
reads prefer the member in the cloud artifactweave runs in. Choose it for provider
independence: any one cloud's outage, or leaving a cloud altogether, costs no blob and stops no
upload. Artifactory's sharding provider cannot do this, because its shards are all one storage
kind. Choose a `multi-region` scenario instead when one provider is enough and you want
resilience across its regions without paying internet egress for every copy.

## What it deploys

- An S3 bucket through [`modules/aws/storage`](../../modules/aws/storage/README.md), named
  `<name>-<suffix>`, with a bucket policy granting `app_role_arn` its objects.
- A Cloud Storage bucket through [`modules/gcp/storage`](../../modules/gcp/storage/README.md),
  with the same name, granting `app_service_account` object rights on it.
- A storage account and its container through
  [`modules/azure/storage`](../../modules/azure/storage/README.md), named after the first 16
  letters and digits of `name` and the suffix, granting `app_principal_id` and each of
  `additional_principals` Storage Blob Data Contributor on it.
- `random_id.suffix`: eight hex characters shared by the three names, so they are globally
  unique.
- `azurerm_resource_group.this`: `<name>-rg` in `azure_location`, unless `resource_group_name`
  names an existing group.

No store replicates on its own. A cloud's own copies are invisible to the mirror and never
count toward its quorum.

## How it behaves

- **Uploads** go to all three members in parallel and succeed once `write_quorum` of them hold
  the blob. With the default of 2, every acknowledged upload is on two providers before the
  client hears it succeeded. The third copy finishes within `blob.mirror.member_timeout` (30 s
  by default), or repair copies it later.
- **Reads and redirects** prefer the member in this instance's zone, the home cloud's, then the
  member with the lowest observed latency, and fall back to another when a member fails. A
  `downloadRedirect` repository signs a URL only on a member that holds the blob, so a redirect
  never lands on a member that lacks it, even when the only copies are on other providers.
- **Draining** a member (`write_enabled: false` on its store) stops new copies on it while
  reads go on; enabling it again lets repair copy what it missed.
- **Losing a provider** stops nothing with `write_quorum: 2`: uploads land on the other two,
  reads are served from them, and repair fills the gap once the provider is back. Losing two
  stops uploads unless `write_quorum` is 1, while reads go on from the one left.
- **Leaving a provider** is a configuration change: drain its member, wait until its
  `soleCopies` in `GET /api/storageinfo` is 0, then remove it from `blob.mirror.members`; the
  other two hold everything (decision 0028, "Removing a member").

## Prerequisites

- Terraform 1.9 or later, and a login on each cloud that Terraform's providers find: AWS
  credentials the SDK chain finds (`aws login`, a profile), Application Default Credentials
  (`gcloud auth application-default login`) and an Azure CLI login (`az login`).
- Rights to create, on AWS, an S3 bucket with its policy, encryption, lifecycle and public
  access settings; on GCP, a bucket and its IAM bindings in `project_id`, where the Cloud
  Storage API is enabled; on Azure, a storage account and role assignments in the resource
  group (Contributor and Role Based Access Control Administrator on it, or Owner; creating the
  group needs Contributor on the subscription).
- `Microsoft.Storage` registered in the subscription. The azurerm provider registers nothing
  (`resource_provider_registrations = "none"`); `az provider register --namespace
  Microsoft.Storage` does it once.
- The identity artifactweave uses on each cloud, created elsewhere: an IAM role
  (`app_role_arn`), a service account (`app_service_account`) and a managed identity or service
  principal (`app_principal_id`). This scenario grants each one its store; it creates none of
  them, and it does not federate them: artifactweave running in one cloud reaches the other two
  with credentials their SDK chains find (below).

### Getting the code

Copy this directory together with `modules/aws/`, `modules/gcp/`, `modules/azure/` and
`.terraform.lock.hcl`, keeping the `../../modules` layout, or point each module at the
repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/storage?ref=v0.1.0" # x-release-please-version
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/gcp/storage?ref=v0.1.0" # x-release-please-version
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/azure/storage?ref=v0.1.0" # x-release-please-version
```

## Inputs to set

Each cloud-specific input names its cloud, so one root can hold all three.

```hcl
# terraform.tfvars
project_id          = "example-project"
subscription_id     = "00000000-0000-0000-0000-000000000000"
name                = "artifactweave-example"
home_cloud          = "aws"
aws_region          = "eu-west-2"
gcp_location        = "europe-west2"
azure_location      = "uksouth"
app_role_arn        = "arn:aws:iam::111122223333:role/artifactweave"
app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"
app_principal_id    = "00000000-0000-0000-0000-00000000000a"

# Optional
# write_quorum          = 1
# store_prefix          = "artifacts/"
# resource_group_name   = "platform-rg"
# additional_principals = { operator = { principal_id = "<object ID>", principal_type = "User" } }
# expiring_prefixes     = { "scratch/" = 1 }
# force_destroy         = true
# labels                = { team = "platform" }
```

```bash
terraform init
terraform apply
```

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one store per cloud,
named after its backend and placement, each in a zone that names its cloud
(`aws-eu-west-2`, `gcp-europe-west2`, `azure-uksouth`), and a mirror over them whose `zone` is
the home cloud's. The stores name no secrets: each backend uses its cloud's credential chain.
The example below is the rendered output for the example inputs above, with the random suffix
`0a1b2c3d`; the `readme_example` test fails if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: mirror
  stores:
    s3_eu_west_2:
      backend: s3
      zone: aws-eu-west-2
      s3:
        bucket: artifactweave-example-0a1b2c3d
        region: eu-west-2
    gcs_europe_west2:
      backend: gcs
      zone: gcp-europe-west2
      gcs:
        bucket: artifactweave-example-0a1b2c3d
        google_access_id: artifactweave@example-project.iam.gserviceaccount.com
    azure_uksouth:
      backend: azure
      zone: azure-uksouth
      azure:
        account: artifactweaveexa0a1b2c3d
        container: artifactweave
        credential: default
  mirror:
    members:
      - s3_eu_west_2
      - gcs_europe_west2
      - azure_uksouth
    zone: aws-eu-west-2
    write_quorum: 2
```
<!-- END_CONFIG_EXAMPLE -->

### Running artifactweave with it

```bash
terraform output -raw artifactweave_config > artifactweave.yaml
export ARTIFACTWEAVE_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: it seals stored secrets
export ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD='choose one'      # read on first start only
artifactweave serve --config artifactweave.yaml
```

artifactweave needs a credential for every cloud, wherever it runs:
- **AWS:** the SDK chain finds `app_role_arn`'s credentials, from an instance profile or pod
  role in AWS, or a web identity or profile elsewhere.
- **GCP:** Application Default Credentials for `app_service_account`, from the attached service
  account in GCP, or a workload identity federation credential configuration in
  `GOOGLE_APPLICATION_CREDENTIALS` elsewhere. It signs download URLs through the IAM
  Credentials API, so it needs `roles/iam.serviceAccountTokenCreator` on itself.
- **Azure:** the Azure SDK's default chain finds `app_principal_id`, from a managed identity in
  Azure, or a workload identity federated credential or service principal in the environment
  elsewhere.

Set `blob.mirror.zone` on each instance to the zone of the cloud it runs in, so its reads go to
the nearest member. A `downloadRedirect` repository answers downloads with a 302 to a signed URL
on a member that holds the blob, preferring the zone's: `storage_host` is the home cloud's
endpoint. The [configuration reference](../../../../../docs/operations/configuration-reference.md)
lists every key.

## Security

Each store carries its module's invariants:
[S3](../../modules/aws/storage/README.md#invariants) (public access blocked, ACLs disabled, TLS
required, S3-managed encryption),
[Cloud Storage](../../modules/gcp/storage/README.md#invariants) (uniform bucket-level access,
public access prevented, Google-managed encryption) and
[Azure Blob](../../modules/azure/storage/README.md#invariants) (no public containers, HTTPS and
TLS 1.2 only, Shared Key disabled, encryption at rest), and abandoned uploads expire in all
three. Each app identity holds object rights on its own cloud's store only: it can read, write,
list and delete objects there, and cannot change the store's settings, policy or grants. No
identity of one cloud is granted anything on another.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AWS-0089 | `aws_s3_bucket.blobs` (`aws/storage`) | No access logging: CloudTrail records the management calls, and server access logs need a second bucket. |
| AWS-0090 | `aws_s3_bucket.blobs` (`aws/storage`) | No versioning: blobs are content-addressed and never rewritten, and the mirror, not S3, keeps the other copies. |
| AWS-0132 | `aws_s3_bucket_server_side_encryption_configuration.blobs` (`aws/storage`) | S3-managed keys; a KMS key would add a monthly fee and a key policy to manage. |
| GCP-0007 | `google_storage_bucket_iam_member.app` (`gcp/storage`) | `roles/storage.objectAdmin` is bound to one bucket, not the project, and the backend needs all of it, deleting the parts it composes included. |
| GCP-0066 | `google_storage_bucket.blobs` (`gcp/storage`) | Google-managed encryption keys; a customer-managed key would add a KMS key ring and its grants to manage. |
| GCP-0077 | `google_storage_bucket.blobs` (`gcp/storage`) | No access-log bucket: Cloud Audit Logs record the administrative calls. |
| GCP-0078 | `google_storage_bucket.blobs` (`gcp/storage`) | No versioning: blobs are content-addressed and never rewritten, and the mirror, not Cloud Storage, keeps the other copies. |
| AZU-0012 | `azurerm_storage_account.blobs` (`azure/storage`) | The account takes connections from any network, because artifactweave and the clients its signed URLs serve have no fixed addresses; with Shared Key off, every request still needs an Entra ID token or a user delegation SAS. |
| AZU-0057 | `azurerm_storage_account.blobs` (`azure/storage`) | No Storage Analytics logs: the activity log records the management calls. |
| AZU-0058 | `azurerm_storage_account.blobs` (`azure/storage`) | LRS on purpose: the account keeps one copy, and the mirror keeps the others on AWS and GCP. |
| AZU-0060 | `azurerm_storage_account.blobs` (`azure/storage`) | Microsoft-managed keys; a customer-managed key would add a Key Vault and its access policies to manage. |
| AZU-0061 | `azurerm_storage_account.blobs` (`azure/storage`) | No infrastructure (double) encryption: service-side encryption already applies. |

## Cost

### Steady state

Storage is billed by each provider: three members store everything three times (roughly $0.02
per GB-month each for S3 Standard, Cloud Storage Standard and hot LRS block blobs in the
London regions). Egress dominates: artifactweave writes each copy from where it runs, so every
GB uploaded leaves the home cloud twice as internet egress (roughly $0.08 to $0.12 per GB on
each provider's first tier), and every repair copy and every download served by another
cloud's member is egress from that cloud. Ingress is free on all three. Requests are billed per
store. There is no fixed or hourly charge.

### Per test run

About two to five cents: a proof stores well under 100 MiB on each cloud for minutes, and its
cost is mostly the test's downloads leaving the three clouds and the copies artifactweave
writes from the runner, which every provider receives as free ingress.

## Teardown

`terraform destroy` removes the two buckets and the storage account, their policies and grants,
and, when this root created it, the resource group. It fails on a bucket that still holds
objects unless `force_destroy` is true, which deletes them with the bucket; deleting the
storage account deletes its blobs regardless. A resource group passed in through
`resource_group_name` stays, and so do the three app identities, which are not this
scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the composition:
  `readme_example` renders the example above from the example inputs; `ci_run` uses the longest
  instance a proof produces, checking that the names fit S3's, Cloud Storage's and Azure's
  limits, the home cloud's member leads, and the outputs reap and `test/platform` read; others
  check the member order for each home cloud, a zone per member, that the inputs reach every
  store, and the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, any of its three modules or `mirror_test.go`) runs as `multicloud`: one job
  authenticates as the deployer on all three clouds, applies an instance and runs
  artifactweave against it, and runs `test/platform`: all three members hold each blob, a
  redirect prefers the home cloud's member, an upload and every read succeed with that member
  drained while the redirect for the new blob lands on another provider's member that holds
  it, and repair copies the new blob back once the member is enabled again. It then destroys
  the instance, applies and destroys another from its remote state alone, and checks that reap
  removes an expired one and that nothing of the run is left on any cloud. The instances' state
  lives in the GCP bootstrap's state bucket, under `scenarios/multicloud/mirror/`.
- **Harness outputs** (`harness.tf`): `expires_at`, the label reap reads from the state, and
  `mirror_members`, the members `test/platform` checks redirects against.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks against
  an instance in the lab, with both the AWS and the Azure guards first
  ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `project_id` | `PLATFORM_GCP_PROJECT` | `LAB_GCP_PROJECT` |
| `subscription_id` | `PLATFORM_AZURE_SUBSCRIPTION_ID` | `LAB_AZURE_SUBSCRIPTION` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `artifactweave-awscn-<instance>`, inside the bucket names the AWS deployer may manage | `artifactweave-mc` |
| `home_cloud` | `gcp` | `aws` |
| `aws_region` | `PLATFORM_AWS_REGION` | `LAB_AWS_REGION` |
| `gcp_location` | `europe-west2` | `europe-west2` |
| `azure_location` | `uksouth` | `uksouth` |
| `resource_group_name` | `PLATFORM_AZURE_RESOURCE_GROUP`, the scenarios resource group where the Azure deployer's rights end | none: the root creates `artifactweave-mc-rg` |
| `app_role_arn` | `PLATFORM_AWS_ROLE_ARN`, the AWS deployer | the AWS bootstrap's conformance role, in the signed-in account |
| `app_service_account` | `PLATFORM_GCP_SERVICE_ACCOUNT`, the GCP deployer | the GCP bootstrap's conformance service account |
| `app_principal_id` | the Azure deployer, from the `oid` claim of its access token | the Azure bootstrap's conformance identity |
| `additional_principals` | none | you, as `operator` |
| `labels` | `$SCENARIO_LABELS`: `artifactweave-scenario`, `instance`, `expires-at` | none |
| `force_destroy` | `true` | `true` |

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| aws | ~> 6.67 |
| azurerm | ~> 5.9 |
| google | ~> 8.6 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| azurerm | 5.9.0 |
| random | 3.9.1 |

### Modules

| Name | Source | Version |
| ---- | ------ | ------- |
| aws | ../../modules/aws/storage | n/a |
| azure | ../../modules/azure/storage | n/a |
| gcp | ../../modules/gcp/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [azurerm_resource_group.this](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/resource_group) | resource |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| additional\_principals | Further principals that get Storage Blob Data Contributor on the storage account, by a static name: an operator who runs artifactweave or mints user delegation SAS from a workstation, for one. principal\_type (User, Group or ServicePrincipal) is optional. | ```map(object({ principal_id = string principal_type = optional(string) }))``` | `{}` | no |
| app\_principal\_id | Object ID of the identity artifactweave uses on Azure (a managed identity, or a service principal). It gets Storage Blob Data Contributor on the account. | `string` | n/a | yes |
| app\_role\_arn | ARN of the IAM role artifactweave uses on AWS. The bucket policy grants it the bucket's objects. | `string` | n/a | yes |
| app\_service\_account | Email of the service account artifactweave uses on GCP. It gets object rights on the bucket, and signs download URLs (blob.stores.<name>.gcs.google\_access\_id). | `string` | n/a | yes |
| aws\_region | AWS region of the S3 member. | `string` | n/a | yes |
| azure\_location | Azure region of the storage account member, for example uksouth. The resource group this root creates is there too. | `string` | n/a | yes |
| container | Blob container in the storage account (blob.stores.<name>.azure.container). | `string` | `"artifactweave"` | no |
| expiring\_prefixes | Key prefixes, from each member's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the S3 and Cloud Storage buckets with every object still in them. Off, a destroy fails while a bucket holds anything. Deleting a storage account deletes its blobs regardless. | `bool` | `false` | no |
| gcp\_location | GCP region of the Cloud Storage member, for example europe-west2. A single region: a dual-region bucket's own copies are invisible to the mirror. | `string` | n/a | yes |
| home\_cloud | The cloud artifactweave runs in: aws, gcp or azure. Its member is the instance's zone (blob.mirror.zone), which reads prefer and whose store signs download URLs first. | `string` | n/a | yes |
| labels | Labels for every resource that takes them: tags on AWS and Azure, labels on GCP. Cloud Storage's rules apply to all of them: keys start with a lowercase letter, and keys and values hold lowercase letters, digits, underscores and hyphens, 63 characters at most. | `map(string)` | `{}` | no |
| name | Start of every resource name. The S3 and Cloud Storage buckets are <name>-<8 hex>, which must fit 63 characters; the storage account takes the name's first 16 letters and digits, lowercased, then the same 8 hex; the resource group this root creates is <name>-rg. | `string` | n/a | yes |
| project\_id | GCP project that holds the Cloud Storage bucket. Nothing is created anywhere else on GCP. | `string` | n/a | yes |
| resource\_group\_name | An existing Azure resource group to create the storage account in. Null creates <name>-rg in azure\_location. | `string` | `null` | no |
| store\_prefix | Key prefix artifactweave writes under in every member (blob.stores.<name>.<backend>.prefix). Empty writes at each bucket's or container's root. | `string` | `""` | no |
| subscription\_id | Azure subscription that holds the storage account. Nothing is created anywhere else on Azure. | `string` | n/a | yes |
| write\_quorum | Copies stored before an upload succeeds (blob.mirror.write\_quorum). 2 means every upload is on two providers before it is acknowledged, so losing any one provider loses nothing, and uploads still go on while one is down; 1 keeps them going with two down. | `number` | `2` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml: a mirror of the S3 bucket, the Cloud Storage bucket and the storage account, preferring the home cloud's. Each store authenticates through its cloud's own credential chain, as that cloud's app identity. |
| aws\_bucket | The S3 bucket. |
| azure\_account | The storage account. |
| expires\_at | For the test harness: the expires-at label (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| gcp\_bucket | The Cloud Storage bucket. |
| mirror\_members | For the test harness: the mirror's members in order (the home cloud's first), each with its store name, zone, and what a signed URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it. |
| resource\_group | The resource group holding the storage account. |
| storage\_host | Where a downloadRedirect repository's signed URLs point first: the home cloud's member, which signs once it holds the blob. |
<!-- END_TF_DOCS -->
