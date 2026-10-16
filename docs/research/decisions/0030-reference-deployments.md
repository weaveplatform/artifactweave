# 0030: Reference deployments and platform testing

Status: Accepted

## Context

artifactweave supports fs, S3, GCS and Azure blob stores, a quorum mirror across them, and
(decision [0029](0029-cdn-delivery-and-download-redirect.md)) CDN delivery on all three
clouds. Operators need a tested way to stand each combination up, and the project needs to
prove each combination works against real cloud services, not only fakes and emulators.

The maintainer decided on 2026-10-14:
- every Terraform root the Makefile uses lives under `deploy/`;
- infrastructure goes in per-cloud, per-scenario roots, and app configuration through the
  JFrog providers in `deploy/artifactweave/terraform/<scenario>`;
- real-cloud runs, teardown included, happen only with the maintainer's approval;
- the Azure CDN scenario uses Front Door Premium with Private Link;
- the DNS subdomain is `artifact.weaveplatform.run`;
- the cloud accounts are labs.

On 2026-10-15 the maintainer set the goal for every reference deployment: it is written
once, and serves both as the acceptance test for a deployment path on a real cloud and as
the documentation consumers read and copy for that path. A review of the first nine
scenarios found the pattern applied inconsistently: some composed shared modules and some
declared storage inline, interfaces and outputs differed between scenarios and clouds, lab
identifiers and lookups sat in the Terraform, harness-only inputs were mixed into the
consumer documentation, and the READMEs followed three different shapes. The maintainer
chose modules composed by thin scenarios, applied consistently, with the lab harness kept
apart from the references.

## Decision

### Layout

```text
deploy/
  .gitignore                      Terraform working files, state, plans, .local/, .secrets/,
                                  local_override.tf, ci_backend.tf; no per-root .gitignore
  check.sh                        the deploy-terraform checks, including the contract linter
  ci/                             the lab harness; nothing here is a reference
    README.md                     bootstraps, environments, driver, local runner
    lab.mk                        every lab constant: GCP project, Azure subscription and
                                  bootstrap resource group, AWS region, zone ID and zone name
    lab-vars.sh                   Terraform variables for an operator run on one cloud
    scenario.mk                   make targets shared by every scenario
    bootstrap.mk                  make targets shared by the bootstraps
    scenario-ci.sh                the CI driver: apply, test, destroy, reap, check-clean
    prove-matrix.sh               which scenarios a pull request proves
    prove-matrix_test.sh          table test of prove-matrix.sh
    lint-contract.sh              the scenario, module, app-layer and bootstrap contract
    local.sh                      the local runner for any applied scenario
    github-environments.sh        creates the cloud-conformance and cloud-platform environments
    check-account.sh              refuses an AWS run outside the lab account
    check-subscription.sh         refuses an Azure run outside the lab subscription
    terraform/aws/ gcp/ azure/    the bootstraps: CI identities, remote state, DNS zone,
                                  APIs, budget
  platform/                       consumer references only
    README.md                     catalogue, choosing a scenario, copying one
    SCENARIO-TEMPLATE.md          the README every scenario follows
    MODULE-TEMPLATE.md            the README every module follows
    terraform/
      modules/<cloud>/<capability>  per-cloud child modules: storage, replication (AWS), cdn
      aws/ gcp/ azure/
        single-bucket/            one private bucket or storage account
        multi-region/             two regional stores as artifactweave mirror members
        geo-replicated/           one store with the cloud's own replication
        cdn/                      a store behind CloudFront, Cloud CDN or Front Door
      multicloud/
        mirror/                   one mirror with a member on each cloud
  artifactweave/
    README.md                     managing artifactweave with the JFrog Terraform providers
    TEMPLATE.md                   the README every app-layer root follows
    terraform/<scenario>/         repositories, identity, oidc, cdn-repositories, ...
test/terraform/run.sh             the app-layer driver: builds a -cover server, mints a token,
                                  applies each deploy/artifactweave root, requires an empty
                                  plan, destroys
```

- A bootstrap is not a reference. An operator applies it once per cloud and it outlives
  every scenario: it holds the identities CI uses, the remote state store, the DNS zone,
  enabled APIs and a budget. `make destroy` refuses on a bootstrap.
- Everything under `deploy/platform` is written for a consumer. Anything that exists only
  for the lab or its harness lives under `deploy/ci`, in a scenario's `harness.tf`,
  `Makefile` or `ci-vars.sh`, or in the README section "How this scenario is tested".
- Compute hosting of artifactweave itself, when it is added, is a further
  `deploy/platform` scenario.

### Modules and scenarios

- **Capabilities are modules.** Each cloud capability is written once as a child module
  under `modules/<cloud>/<capability>`, and its security invariants are tested once, in
  that module's own `tests/`. A scenario never declares a capability's resources itself.
- **Scenarios are compositions.** A scenario root composes modules, renders the
  artifactweave configuration it needs, and documents the deployment path. Its tests check
  only how it composes its modules.
- **Modules are per cloud.** There is no cross-cloud abstraction module: each cloud's
  storage, replication and CDN resources differ too much for one interface to help, and a
  reference should read like the cloud's own idiom.
- **Modules never call modules,** so a module's dependants are exactly the scenarios that
  source it.
- **Module paths are public API** from the first release. Renaming or removing a module, or
  a required input of one, is a breaking change (`feat!`).

| Module | Creates | Inputs (default) |
|---|---|---|
| `aws/storage` | one private S3 bucket and its policy | `name`, `region`, `principals` (map of role ARNs, at least one), `store_prefix` (`""`), `versioning` (false), `expiring_prefixes` ({}), `force_destroy` (false), `tags` ({}) |
| `aws/replication` | S3 cross-region replication from one bucket to another, and its IAM role | `name`, `source_bucket` and `destination_bucket` (`aws/storage` outputs; `source` is reserved in a `module` block), `prefix`, `replicate_deletes` (false), `replication_time_control` (false), `storage_class` ("STANDARD"), `permissions_boundary_arn` (null), `iam_role_path` (null), `tags` ({}) |
| `gcp/storage` | one private GCS bucket and its grants | `name`, `location`, `data_locations` ([], zero or two), `rpo` (null), `principals` (map of service-account emails), `store_prefix` (`""`), `expiring_prefixes` ({}), `force_destroy` (false), `labels` ({}) |
| `azure/storage` | one storage account, its container, lifecycle policy and grants | `account_name`, `resource_group_name`, `location`, `replication_type` ("LRS"), `container` ("artifactweave"), `store_prefix` (`""`), `principals` (map of `{principal_id, principal_type}`, type optional), `expiring_prefixes` ({}), `tags` ({}) |

Every storage module enforces, and tests, the same baseline in its cloud's terms: no public
access; TLS only; encryption at rest; uniform, identity-based access (S3 bucket owner
enforced, GCS uniform bucket-level access, Azure Shared Key disabled and Entra ID by
default); the principals receive only the object and listing rights artifactweave uses, on
that store only; `<store_prefix>_uploads/` expires after 2 days; each `expiring_prefixes`
entry expires after its days; incomplete S3 multipart uploads abort after 1 day; and, when
versioning is on, noncurrent versions expire after 1 day. Every resource that takes tags or
labels receives them. The AWS modules place each resource with the provider's per-resource
`region` argument (from provider 6.0), so one provider configuration serves several regions
without aliases. Policies are written with `jsonencode`, which mocked tests can read.
Modules normalise `store_prefix` as the blob backends do, adding the trailing slash.

On GCP and Azure, replication is a property of one bucket or account (`location` with
`data_locations` and `rpo`; `replication_type`), so it is a storage input. On AWS it spans two
buckets and needs its own IAM role, so it is the separate `aws/replication` module.

CDN capabilities are a flat `modules/<cloud>/cdn` beside storage, composed by the `cdn`
scenarios. Storage interfaces only grow additively for them: on AWS the CDN module outputs
the origin access control's bucket-policy statement and the scenario passes it to
`aws/storage` (S3 allows one policy per bucket); on GCP the CDN module grants
`cloud-cdn-fill` read access itself, because IAM members are not authoritative; on Azure
storage gains `public_network_access_enabled` and the CDN module owns the Private Link
origin. The multicloud mirror prefixes every cloud-specific input with its cloud
(`aws_region`, `gcp_location`, `azure_location`).

### The scenario contract

Every scenario root `deploy/platform/terraform/<cloud>/<scenario>` (where `<cloud>` is `aws`,
`gcp`, `azure` or `multicloud`) follows these rules. `deploy/ci/lint-contract.sh` enforces
them by ID.

| ID | Rule |
|---|---|
| S1 | **Files.** Required: `versions.tf`, `variables.tf`, `main.tf`, `outputs.tf`, `harness.tf`, `README.md`, `.terraform.lock.hcl`, at least one `tests/*.tftest.hcl`, `Makefile`, `ci-vars.sh`. Optional: `moved.tf`. Forbidden: `backend.tf`, any `*.tfvars*`, `.gitignore`, and container or credential scripts (`credentials.sh`, `up.sh`, `smoke.sh`, `compose.yaml`). |
| S2 | **Composition.** The only `resource` types a root may declare are `random_id`, `random_string` and `azurerm_resource_group`. Everything else comes through `module` blocks whose source is `../../modules/<cloud>/<capability>`. No `data` blocks. |
| S3 | **Providers.** Provider blocks appear only in `versions.tf`, with no `alias`; the multicloud scenarios declare one per provider, still without aliases. The account scope is a required input (`project_id`, `subscription_id`); AWS takes it from credentials. `required_version` is `>= 1.9`, for validation rules that compare variables. On azurerm, `storage_use_azuread = true` and `resource_provider_registrations = "none"`. |
| S4 | **Inputs.** Names follow the vocabulary table. The account scope, `name`, placement and the app principal are required and have no default. Every variable has a `type` and a `description`. No default equals a lab value, and no variable exists only for the harness. |
| S5 | **Outputs.** Consumer outputs live in `outputs.tf`; harness-only outputs live in `harness.tf`, each description beginning "For the test harness:", so a consumer can delete the file without breaking anything. `artifactweave_config` never contains a secret. |
| S6 | **Lab values.** No lab literal appears in any `.tf` under `deploy/platform`. The deny-list is every value in `deploy/ci/lab.mk` plus the lab's fixed names: `artifactweave-conformance`, `artifactweave-deployer`, `artifactweave-bootstrap-rg`, `artifactweave-scenarios-rg`, `artifactweave-scenario-boundary`, `/artifactweave-scenarios/` and `weaveplatform.run`. |
| S7 | **`ci-vars.sh`.** Only comments and `export TF_VAR_<name>=…` lines, and every `<name>` is declared in `variables.tf`, because Terraform ignores an environment variable for an undeclared input without an error. It sets `TF_VAR_name="${NAME_PREFIX}${INSTANCE}"`, the cloud's tags or labels from `$SCENARIO_LABELS`, and `TF_VAR_force_destroy=true` wherever `force_destroy` is declared. |
| S8 | **`Makefile`.** Only the scenario's own `TF_VARS`, then an include of `deploy/ci/scenario.mk`. No lab literal: the guard and the lab values come from `scenario.mk`, `lab.mk` and `lab-vars.sh`. |
| S9 | **`README.md`.** Its H2 headings are exactly those of `deploy/platform/SCENARIO-TEMPLATE.md`, in order. It has a YAML example between `<!-- BEGIN_CONFIG_EXAMPLE -->` and `<!-- END_CONFIG_EXAMPLE -->`, a git source line carrying `x-release-please-version`, and `<!-- BEGIN_TF_DOCS -->` under `## Reference`. Every `#trivy:ignore:<ID>` in the root or in a module it calls is listed under "Accepted findings". Every `TF_VAR` set by `ci-vars.sh` or the `Makefile` is listed under "How this scenario is tested". No em-dash. |
| S10 | **Tests.** Mocked tests check composition only; module internals belong to the module's tests. Every scenario has `run "readme_example"`, which applies the README's example inputs with the random suffix overridden and asserts that `yamldecode` of `artifactweave_config` equals `yamldecode` of the README's example, so the documentation cannot drift from the code. Every scenario has a run shaped like a CI run, with the longest instance a proof produces, asserting name lengths and the outputs that reap and `test/platform` read. No `mock_data` blocks, since a scenario looks nothing up. |
| S11 | **`moved` blocks** live only in `moved.tf`, each with a comment naming the state it keeps. |

**Inputs.** Each input uses its cloud's own word, singular for one store and a list for a
mirror, whose first entry is the instance's own zone.

| Concept | AWS | GCP | Azure | Required |
|---|---|---|---|---|
| Account scope | from credentials | `project_id` | `subscription_id` | yes |
| Start of every resource name | `name` | `name` | `name` | yes |
| One store's placement | `region` | `location` | `location` | yes |
| Mirror members, in order | `regions` | `locations` | `locations` | yes, mirror only |
| Cloud replication | `replica_region`, `replication_time_control` (false) | `data_locations` ([]), `turbo_replication` (false) | `replication_type` ("RAGZRS") | geo only |
| The identity artifactweave runs as | `app_role_arn` | `app_service_account` | `app_principal_id` | yes |
| More data-plane principals | n/a | n/a | `additional_principals` ({}) | no |
| Tags or labels | `tags` | `labels` | `tags` | no ({}) |
| Key prefix | `store_prefix` (`""`) | `store_prefix` (`""`) | `store_prefix` (`""`) | no |
| Container | n/a | n/a | `container` ("artifactweave") | no |
| Extra expiring prefixes, to days | `expiring_prefixes` ({}) | `expiring_prefixes` ({}) | `expiring_prefixes` ({}) | no |
| Destroy with contents | `force_destroy` (false) | `force_destroy` (false) | n/a: deleting an account deletes its blobs | no |
| Resource group | n/a | n/a | `resource_group_name` (null creates `<name>-rg`) | no |
| Roles the scenario creates | `permissions_boundary_arn` (null), `iam_role_path` (null) | n/a | n/a | no |
| Mirror quorum | `write_quorum` (1) | `write_quorum` (1) | `write_quorum` (1) | mirror only |

- `write_quorum` is validated against the number of members.
- The app principal is the identity a consumer's artifactweave runtime uses: an instance
  profile or workload role, a workload identity service account, a managed identity. A
  scenario never looks it up; the caller always passes it.
- Azure alone takes `additional_principals`, keyed by static names so the plan is known
  before the IDs are. An operator who mints user delegation SAS tokens or runs the
  conformance tests from a workstation needs a data role on the account, and Azure has no
  counterpart to impersonating a service account as GCP does, while an AWS operator acts
  through their own IAM.
- `force_destroy` defaults to false, so a consumer's `terraform destroy` never empties a
  bucket by surprise. CI sets it true (S7).

**Outputs.**

| Output | single-bucket | multi-region | geo-replicated | File |
|---|---|---|---|---|
| `artifactweave_config` | yes | yes | yes | `outputs.tf` |
| `storage_host`: where a `downloadRedirect` repository's signed URLs point | yes | the first member | the primary | `outputs.tf` |
| `bucket` and `region` (AWS), `bucket` (GCP), `account` and `container` (Azure) | yes | n/a | yes | `outputs.tf` |
| `buckets` or `accounts`, a map by placement | n/a | yes | n/a | `outputs.tf` |
| `replica_bucket`, `replica_region`, `replicated_prefix`, `failover_artifactweave_config` | n/a | n/a | AWS | `outputs.tf` |
| `location`, `rpo` | n/a | n/a | GCP | `outputs.tf` |
| `replication_type`, `secondary_location`, `secondary_storage_host` | n/a | n/a | Azure | `outputs.tf` |
| `resource_group` | Azure | Azure | Azure | `outputs.tf` |
| `expires_at`: the instance's `expires-at` label, which reap reads from state | yes | yes | yes | `harness.tf` |
| `mirror_members`: each member's store and host, which `test/platform` reads | n/a | yes | n/a | `harness.tf` |
| `service_account`: the app service account, which `make conformance` passes to the real-GCS tests as their signing identity | GCP | n/a | n/a | `harness.tf` |

`artifactweave_config` renders the sections of `artifactweave.yaml` the scenario needs:
`blob` for every scenario, and `cdn` as well for the CDN scenarios. Outputs that only echo an
input (`store_prefix`, the app principal, the account scope) are not outputs, except in
`harness.tf` where the harness reads one.

**Lab values.**

| Where | Holds |
|---|---|
| `deploy/ci/lab.mk` | Every lab constant, included by `scenario.mk` and `bootstrap.mk`. |
| `deploy/ci/lab-vars.sh <cloud>` | The variables an operator run needs, resolved with the operator's CLI: the account scope; the bootstrap's conformance identity as the app principal; on Azure, the signed-in operator as an additional principal. `force_destroy=true`. |
| The scenario's `Makefile` | Only what is particular to the lab's instance of that scenario. The `single-bucket` Makefiles keep the conformance stores' names, `store_prefix=local/` and `expiring_prefixes` for `conformance/`. |
| The scenario's `ci-vars.sh` | The variables a CI run passes, from the `PLATFORM_*` environment. |

`scenario.mk` derives the cloud from the directory and runs the guard for it:
`check-account.sh` with the lab's zone ID and zone name on AWS, `check-subscription.sh` with
the lab's subscription on Azure. In CI the Azure app principal is the deployer, whose object
ID comes from the `oid` claim of its access token, validated as a GUID, because the
deployer cannot read the directory.

**The `conformance/` prefix.** The real-cloud conformance tests write under `conformance/` in
the `single-bucket` stores only (`pkg/blob/{s3,gcs,azure}/real_test.go`). Its 1-day expiry
therefore reaches those stores alone, through the `single-bucket` Makefiles'
`expiring_prefixes`, and no module or scenario carries it.

**Labels and names.**
- Tags and labels reach every resource that takes them. CI passes `artifactweave-scenario`,
  `instance` and `expires-at` (epoch seconds).
- Every resource name begins with `name`. Azure storage account names are `name` in
  lowercase with everything but letters and digits removed, cut so that it fits Azure's 24
  characters with the random suffix and any region code.
- CI's name prefix is one `NAME_PREFIX` in `scenario-ci.sh`: `artifactweave-awscn-` on AWS,
  because the deployer may manage only `artifactweave-*` buckets, and `awscn-` on GCP and
  Azure, because reap finds unlabelled GCP load-balancer resources by that prefix.

**Documentation.**
- Every scenario README follows `deploy/platform/SCENARIO-TEMPLATE.md`: what it deploys,
  how it behaves, prerequisites and how to get the code, the inputs to set, the
  artifactweave configuration and how to run artifactweave with it, security with the
  accepted findings, cost in steady state and per test run, teardown, how the scenario is
  tested, and the generated reference.
- terraform-docs shows module calls (`modules` is not hidden in `.terraform-docs.yml`), and
  the generated tables sit under `## Reference`, at the end of the README.
- Every module README follows `deploy/platform/MODULE-TEMPLATE.md`, listing each invariant
  with the test run that asserts it.

**Consuming the references.**
- A scenario sources its modules by relative path, `../../modules/<cloud>/<capability>`, so
  a consumer can copy the scenario together with `modules/<cloud>/` and its lock file.
- A consumer can instead point each module at the repository,
  `git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/<cloud>/<capability>?ref=vX.Y.Z`.
  Release-please updates the version on every line marked `x-release-please-version`
  through `extra-files`, so the READMEs always name the current release.

### The module, app-layer and bootstrap contracts

| ID | Rule |
|---|---|
| M1 | **Files.** `versions.tf` (`required_providers` only), `variables.tf`, `main.tf`, `outputs.tf`, `README.md`, at least one `tests/*.tftest.hcl`. |
| M2 | No `provider` blocks and no `module` blocks. |
| M3 | Every variable is set in at least one test run's `variables` block. |
| M4 | Policies use `jsonencode`, never `aws_iam_policy_document`. |
| M5 | The README's H2 headings are exactly those of `deploy/platform/MODULE-TEMPLATE.md`, in order; every `#trivy:ignore:<ID>` is listed under "Accepted findings". |
| M6 | "Usage" shows the git source line carrying `x-release-please-version`. |
| A1 | **App-layer files.** Each `deploy/artifactweave/terraform/<scenario>` has `versions.tf`, `variables.tf`, `main.tf`, `outputs.tf`, `README.md` and `.terraform.lock.hcl`. |
| A2 | Its README's H2 headings are exactly those of `deploy/artifactweave/TEMPLATE.md`, in order. |
| A3 | Its providers are pinned exactly, matching `test/terraform/run.sh`. |
| A4 | `test/terraform/run.sh` has a case for it. |
| B1 | Each bootstrap under `deploy/ci/terraform/<cloud>` has a `backend.tf` whose key names its cloud: prefix `bootstrap/gcp`, key `bootstrap/azure.tfstate`, key `bootstrap/aws.tfstate`. |
| G1 | One `deploy/.gitignore`, and no `.gitignore` in any root. |
| G2 | No em-dash in any Markdown file under `deploy/`. |

The app layer configures a running artifactweave rather than a cloud, so it has no
`ci-vars.sh`, `Makefile`, `harness.tf`, rendered configuration, cost section or cloud
proof: `test/terraform/run.sh` in the gated `terraform` job is its test. Its fixture names
(repository keys, user and group names, the GitHub organisation) are inputs, so a consumer
sets their own and the driver passes the fixture's.

### The local runner

`deploy/ci/local.sh` runs any applied scenario on a workstation, called through
`scenario.mk`:

| Target | Does |
|---|---|
| `render` | writes `.local/artifactweave.yaml` from the scenario's `artifactweave_config`, unchanged, and generates the master key and admin password once |
| `credentials` | makes the credentials the configured backends need: an impersonation file for GCS, exported credentials for S3, none for Azure, which uses the signed-in CLI |
| `up` | builds the binary and runs it in the background against `.local/artifactweave.yaml` |
| `smoke` | a 40 MiB round trip and one `downloadRedirect` redirect to `storage_host` |
| `down`, `logs`, `clean` | stop it, follow its log, remove `.local/` |
| `verify` | runs `test/platform` against the scenario exactly as CI does |

The runner uses the scenario's own configuration rather than writing a second one, and runs
the native binary rather than a container, as a production deployment with a workload
identity would.

### Identities and environments

| GitHub environment | Trusted by | Rights | Used by |
|---|---|---|---|
| `cloud-conformance` | a data-plane identity in each cloud | objects in the conformance stores only | `cloud-conformance.yml` |
| `cloud-platform` | a deployer identity in each cloud | create and destroy scenario resources in the lab project, subscription and account | `platform-scenarios.yml`, `platform-auth-check.yml` |

- Every trust matches the immutable subject
  `repo:weaveplatform@336817058/artifactweave@1415179278:environment:<env>` exactly. On GCP,
  one workload identity provider admits only the two environment subjects (its attribute
  condition), and each service account grants `roles/iam.workloadIdentityUser` to one
  subject (`principal://…/subject/<sub>`), not to the provider. On Azure, each identity has
  one federated credential for one subject. On AWS, each role's trust policy matches the
  audience and one subject with `StringEquals`, never `StringLike`.
- `cloud-platform` requires the maintainer's approval and has no branch restriction, so a
  pull request's branch can prove its own scenario before merge. "Prevent self-reviews"
  stays off, so the maintainer can approve a run they dispatched.
- Deployers get least privilege within their lab:
  - GCP: storage admin, load balancer admin (which covers Cloud CDN), Certificate Manager
    editor and service usage consumer on the project, DNS admin on the scenarios' zone only;
    no role that administers the project, its IAM or its APIs.
  - Azure: Contributor and Storage Blob Data Contributor on one resource group,
    `artifactweave-scenarios-rg`, where every scenario instance is created; Role Based Access
    Control Administrator there, limited by condition to assigning Storage Blob Data
    Contributor and Reader; DNS Zone Contributor on the scenarios' zone.
  - AWS: buckets named `artifactweave-*`, CloudFront and ACM, records in the scenarios'
    hosted zone, and roles under `/artifactweave-scenarios/` only. A permissions boundary
    caps the deployer and must be attached to every role it creates, so it cannot grant
    more than it holds; the boundary also keeps it out of the bootstrap's state, roles,
    policies and OIDC provider. A scenario that creates roles therefore takes the boundary
    and path as inputs, which `ci-vars.sh` sets from the deployer's role ARN.
  - AWS's data-plane role holds no policy: the `single-bucket` bucket policy grants it that
    bucket, so its rights leave with the bucket.
- The bootstraps enable the APIs and resource providers scenarios need, so a deployer never
  needs service-usage administration.

### State

- Each bootstrap creates a private, versioned remote state bucket or container: GCS, Azure
  Storage, and S3 with `use_lockfile`.
- Scenario state for CI runs lives at `scenarios/<cloud>/<scenario>/<instance>` in that
  store, so an `apply` dispatch and a later `destroy` dispatch find the same state. A
  scenario root has no backend of its own: the driver writes one into its working copy, and
  a consumer chooses their own.
- A bootstrap starts on local state, because its backend does not exist yet, and moves its
  state into the store it created with `terraform init -migrate-state`. Its committed
  backend block names the store, so the store's name is fixed rather than generated
  (`artifactweave-tfstate-<project number>` on GCP, `awtfstate<subscription prefix>` on
  Azure, `artifactweave-tfstate-<fixed random suffix>` on AWS, so the account ID stays out
  of the repository). Only a first run, `make ci-init CLOUD=<cloud> FIRST_RUN=1`, writes the
  git-ignored `local_override.tf` that replaces the backend with local state; otherwise a
  failed state probe stops the run, so an expired login can never plan a fresh bootstrap.
- The lab's conformance stores are applied from a workstation with local state; their
  `moved.tf` keeps them when their scenario's composition changes.
- State and plan files are never uploaded as workflow artifacts.

### Keys

- CDN signing keys are generated in the workflow job, outside Terraform, and handed to
  artifactweave through a file. Only the CloudFront public key reaches Terraform.
- The Cloud CDN key is added with `gcloud` after apply, because
  `google_compute_backend_bucket_signed_url_key` stores its `key_value` in state as plain
  text and has no write-only variant. If the provider gains one, the scenario switches to it.

### DNS

- Each cloud's bootstrap holds a zone under `artifact.weaveplatform.run`:
  `aws.artifact.weaveplatform.run` (Route 53), `gcp.artifact.weaveplatform.run` (Cloud DNS)
  and `azure.artifact.weaveplatform.run` (Azure DNS). `weaveplatform.run` is served by
  GoDaddy, where the maintainer adds each zone's NS records from the bootstrap's output. The
  three zones were created by CLI before their bootstraps, so the delegation could be set up
  first; each bootstrap imports its zone and Terraform refuses to destroy it.
- The AWS lab is recognised by its hosted zone (`deploy/ci/check-account.sh`, given the zone
  ID and name from `lab.mk`) rather than an account ID, which this public repository does not
  record.
- A CDN host is a sibling of artifactweave's host, never a subdomain of it (for example
  `registry.gcp.artifact.weaveplatform.run` and `cdn.gcp.artifact.weaveplatform.run`), so
  clients drop credentials on the redirect (decision 0029).

### Checks on every pull request

A `deploy-terraform` job, required by the gate, runs `deploy/check.sh` (`make deploy-check`
locally) over `deploy/` and `test/terraform`. Two steps run first, without downloading any
tool:
- **contract:** `deploy/ci/lint-contract.sh` applies the S, M, A, B and G rules to every root
  by its path, and checks itself against fixtures it generates;
- **prove-matrix:** `deploy/ci/prove-matrix_test.sh` checks the proof choices below against a
  table of changed paths.

Then, per root:
- `terraform fmt -check`;
- `terraform validate` with `init -backend=false`;
- tflint with the aws, google and azurerm rulesets pinned in `.tflint.hcl`;
- `trivy config`, with each accepted finding a `#trivy:ignore` comment beside the resource
  stating its reason;
- `terraform test` wherever a root has `tests/`, using mocked providers only;
- a terraform-docs drift check: regenerate each README in a copy and fail when it differs
  from the committed file.

`deploy/check.sh` installs terraform, tflint, trivy and terraform-docs by exact version, each
archive checked against the SHA-256 from its release, rather than through third-party
actions. It walks the roots it finds, not `--recursive` from the repository root, which would
also descend into worktrees and tool caches.

### Real-cloud runs

`platform-scenarios.yml` runs under the `cloud-platform` environment, so every run waits for
the maintainer's approval. It runs on `workflow_dispatch`, with inputs `cloud`, `scenario`,
`instance`, an `action` and `ttl_minutes` (how long until an applied instance's
`expires-at`), and on pull requests as a proof:

| Action | Does |
|---|---|
| `test` | apply with a unique instance and `expires-at`, start artifactweave from the rendered configuration, run the `test/platform` suite, destroy under `always()` |
| `apply` | apply and leave the instance running, for exploration |
| `destroy` | destroy a named instance |
| `reap` | destroy every instance whose `expires-at` has passed, finding resources by label or name prefix |

- `concurrency` is keyed on cloud, scenario and instance, with `cancel-in-progress: false`,
  so an apply is never cut off halfway.
- One driver, `deploy/ci/scenario-ci.sh`, runs every action. Each action works on a fresh
  copy of the scenario, with `modules/` beside it so module sources resolve as they do in the
  repository, and a generated backend for the instance's state. Nothing carries over between
  invocations except that state, so a destroy finds what a separate apply left exactly as a
  later dispatch does. A destroy deletes the emptied state; when the state is empty or
  absent it deletes it and succeeds without initialising, but it fails when a state exists
  and cannot be read, so it never deletes a state it could not check.
- `reap` reads each state's `expires_at` output and destroys the expired instances, each in
  its own process so a failed destroy never goes on to delete its state. It then deletes
  resources that outlived their state: those labelled with a passed `expires-at`, and
  unlabelled ones carrying the `NAME_PREFIX` once they are two hours old and belong to no
  live instance, so a run still applying keeps its resources.
- On AWS reap finds buckets by their tags rather than by name: it lists the `artifactweave-`
  buckets with their regions and reads each one's tags there, because a scenario may span
  regions and the tagging API answers for one region at a time. An orphaned versioned bucket
  is emptied of its versions and delete markers before reap deletes it.
- A dispatch finds only workflows on the default branch, so a pull request runs proofs
  instead, behind the same approval. `prove-matrix.sh` chooses them from the changed paths:

  | A change to | Proves |
  |---|---|
  | a module, `modules/<cloud>/<capability>` | every scenario whose module source ends in that path |
  | a `test/platform` file naming scenarios in `"<cloud>/<scenario>"` literals | exactly those scenarios |
  | any other `test/platform` file | every cloud's `single-bucket` |
  | the driver, `prove-matrix.sh` or the workflow | every cloud's `single-bucket` |
  | a scenario's `*.tf`, lock file or `ci-vars.sh` | that scenario |
  | only a README, `Makefile` or `tests/` | nothing |
  | a pull request labelled `prove-all` | every scenario |

  Each proof runs `test`, an `apply` followed by a `destroy` from the remote state alone, an
  instance applied already expired and a `reap` that removes it, then a check that nothing of
  the run remains. A scenario with a large fixed hourly cost may prove with `test` alone,
  recorded in its README. Each proof has its own run tag and concurrency group, and its
  `reap` is limited to that tag (`REAP_PREFIX`), so proofs on one cloud run in parallel
  without racing for each other's state. Pull requests from forks get no OIDC token and do
  not run.
- `test/platform` is a Go suite of `_test.go` files only, gated on `ARTIFACTWEAVE_SCENARIO`
  like the real-cloud blob tests. Scenario runs are evidence that a deployment works, not
  coverage: they upload no coverage profiles and the gate never waits for them.

### Make targets

| Target | Runs |
|---|---|
| `make platform-<target> SCENARIO=<cloud>/<scenario>` | a target of a scenario: `plan`, `apply`, `destroy`, `render`, `credentials`, `up`, `smoke`, `down`, `logs`, `clean`, `verify`, `conformance`, `github-vars` |
| `make gcp-<target>`, `make azure-<target>`, `make aws-<target>` | the same for that cloud's `single-bucket`, the lab's conformance store |
| `make ci-<target> CLOUD=<cloud>` | a target of a bootstrap: `init`, `plan`, `apply`, `migrate-state`, `github-vars`, and on GCP `undelete-pool` |

### Dependencies

Dependabot's `terraform` ecosystem covers `deploy/platform/terraform/**` and
`deploy/ci/terraform/*` with major versions ignored, since a provider major merged on mocked
tests alone could break a real apply. `deploy/artifactweave` is excluded:
`test/terraform/run.sh` pins its `jfrog/artifactory` and `jfrog/platform` provider zips by
SHA-256, which Dependabot cannot update. `deps-refresh` does not touch Terraform providers
either, so the version, the checksums and the lock file move together by hand, following the
steps in [docs/testing/ci.md](../../testing/ci.md#terraform-scenarios).

### Lab accounts

The accounts behind these environments are labs. The design asks for no operator-hardening
steps, such as separate non-root administrators or MFA ceremonies. The scenarios themselves
still demonstrate private storage, OIDC trust without stored secrets and least-privilege CI
roles, because those are part of what a reference shows.

## Rationale

- **Written once, for two readers.** A scenario that is both the acceptance test and the
  documentation cannot describe a deployment that does not work: the proof applies exactly
  what the README describes, and `readme_example` fails when the README's configuration
  differs from the rendered one.
- **Modules composed by thin scenarios** put each capability's invariants in one place, so a
  hardening change is made once and re-proven on every scenario that uses it. A scenario
  stays short enough to read as a deployment path. The CDN scenarios and the multicloud
  mirror add compositions rather than further copies of a bucket.
- **Consistency is linted, not described.** The first nine scenarios drifted in three
  generations despite a written contract. Rules with IDs that `deploy/check.sh` enforces keep
  every new scenario on the same shape.
- **The lab harness lives apart from the references.** A consumer copying a scenario gets no
  lab identifiers, lookups of our identities or guards for our accounts, and the generated
  tables list only inputs a consumer sets. The harness keeps everything it needs in
  `deploy/ci`, `harness.tf`, the `Makefile` and `ci-vars.sh`.
- **The app principal is required, not looked up,** because every consumer runs artifactweave
  under an identity of their own, and a lookup of ours can only fail for them.
- **`force_destroy` defaults to false** because a consumer's `terraform destroy` should not
  delete their artifacts; CI, which destroys its own test data, opts in.
- **One environment per job decides the environment set.** A job references exactly one
  environment ([research 20](../20-reference-deployments.md) section 4.1). Per-cloud
  environments would leave the multicloud scenario unable to authenticate to all three
  clouds in one job. Splitting data plane from deployer keeps the conformance tests on
  object-level rights while scenario runs get the broader rights they need, behind an
  approval.
- **Remote state follows from separate teardown.** With a destroy dispatched separately from
  its apply, state must outlive the job that created it. Local state in the runner would
  strand every `apply` instance.
- **Bootstraps are split from scenarios** so scenarios can be created and destroyed freely
  without touching the trust that CI depends on. A destroyed GCP workload identity pool's ID
  stays reserved for about 30 days (section 4.3), so recreating trust with each scenario is
  not an option.
- **Keys stay out of state** because state is readable by anyone who can read the backend,
  and two of the obvious resources (`tls_private_key`, the Cloud CDN signed URL key) store
  key material there in plain text (section 5).
- **Runs only with approval, never on a schedule,** was the maintainer's choice: CDN
  scenarios carry fixed hourly costs (Front Door Premium's base fee, a Cloud load balancer's
  forwarding rules), and a scheduled run would spend them without anyone looking at the
  result.
- **Mocked tests on every pull request** catch the invariants that matter most in a reference
  (public access, query-string caching, origin access) without credentials, so forks and
  Dependabot pull requests get them too.
- **Scenario runs are not coverage** because they depend on approval and cloud state; feeding
  them into the gate would make it non-deterministic.
- **A native local runner** uses the scenario's own configuration and the credential chain a
  production runtime would use. A container needed exported sessions, impersonation files
  and, on Azure, a SAS with a second, hand-written configuration that disagreed with the
  scenario's output.
- **Alternatives rejected:**
  - self-contained roots with no modules: each cloud's store and its tests would be written
    once per scenario, three times today and five once the CDN and multicloud scenarios
    exist;
  - one generic multi-cloud storage module: the resources differ too much, and a reference
    should read like the cloud's own idiom;
  - keeping the bootstraps beside the scenarios: they are the lab's CI foundation, which no
    consumer deploys;
  - keeping `test/terraform` as the app-layer home: it is a test asset, while operators need
    references they can copy;
  - the trust inside each scenario root: see the pool ID reservation above.
- **Deviation:** none from Artifactory. JFrog publishes binary-provider templates per cloud,
  not tested deployments; this decision adds deployments around the same storage choices.

## Constraints

- No emulator exists for CloudFront, Cloud CDN or Front Door, so CDN behaviour is proven only
  by real-cloud runs; acceptance uses a fake edge (decision 0029).
- GCP label values allow only lowercase letters, digits, underscores and dashes, so
  `expires-at` holds epoch seconds. Backend buckets, URL maps and target proxies take no
  labels in the Terraform provider, and Compute documents forwarding-rule labels only for
  internal load balancers, so the reaper also matches name prefixes (section 5).
- An Azure identity holds at most 20 federated credentials and no wildcards, so each
  environment needs its own exact credential (section 4.4).
- Required reviewers on environments are available to this repository only because it is
  public (section 4.1).
- Mocked tests need Terraform 1.7 or later, write-only arguments 1.11 or later, and
  validation rules that compare variables 1.9 or later (section 3).
- A consumer who copies a scenario must keep the `../../modules/<cloud>` layout beside it, or
  switch its module sources to the git form.
- CloudFront distributions, Front Door profiles and managed certificates take minutes to tens
  of minutes to create and delete, which bounds how fast a `test` run can be.
- Front Door Premium bills each started hour that a profile exists (section 5), so the Azure
  CDN scenario proves with `test` alone.
- The GCP pool `github-actions` was deleted on 2026-10-14 and undeleted for the bootstrap,
  which imports it with its provider rather than choosing a new ID.
- The DNS zones were created by CLI before the bootstraps, so their delegation at GoDaddy
  could be set up; the bootstraps import them and refuse to destroy them, because a new zone
  would get new name servers.

## Verification

- `make deploy-check` passes on every pull request: the contract linter and its fixtures, the
  prove-matrix table test, fmt, validate, tflint, trivy, the mocked tests and the
  terraform-docs drift check. It fails on an unformatted file, a lint finding, an unrecorded
  scanner finding, a failing mocked test, a stale README or a contract violation.
- Each module's tests assert every invariant in its README's "Invariants" section.
- Each scenario's `readme_example` test asserts that its README's configuration example
  equals the rendered `artifactweave_config`.
- Each scenario has a recorded `platform-scenarios` run that applied, passed `test/platform`
  and destroyed, linked from its pull request, and a CLI check after the run found nothing of
  it left.

  Every scenario passed on contract v2 (epic #189), and each run left nothing behind:

  | Scenario | Proof runs | Pull request |
  |---|---|---|
  | `aws/multi-region`, `gcp/multi-region`, `azure/multi-region` | [38746874394](https://github.com/weaveplatform/artifactweave/actions/runs/38746874394), [38752999957](https://github.com/weaveplatform/artifactweave/actions/runs/38752999957), [38754603808](https://github.com/weaveplatform/artifactweave/actions/runs/38754603808) | #205 |
  | `azure/single-bucket`, `azure/geo-replicated` | [38760230700](https://github.com/weaveplatform/artifactweave/actions/runs/38760230700) | #208 |
  | `gcp/single-bucket`, `gcp/geo-replicated` | [38760335950](https://github.com/weaveplatform/artifactweave/actions/runs/38760335950), [38763804471](https://github.com/weaveplatform/artifactweave/actions/runs/38763804471) | #209 |
  | `aws/single-bucket`, `aws/geo-replicated` | [38761559805](https://github.com/weaveplatform/artifactweave/actions/runs/38761559805), [38763587235](https://github.com/weaveplatform/artifactweave/actions/runs/38763587235) | #210 |

  The #205 runs also proved every cloud's `single-bucket` before its port, and the #208 and
  #210 runs re-proved their cloud's `multi-region` after its module changed.
- After a bootstrap moves or changes, `make ci-plan CLOUD=<cloud>` shows no changes, and lint
  rule B1 keeps each bootstrap's backend key tied to its cloud.
- `make terraform TERRAFORM_MODE=apply` applies each `deploy/artifactweave` root through
  `test/terraform/run.sh` with an empty plan before destroy.

## References

- [Research 20](../20-reference-deployments.md): multi-region storage, Terraform practice,
  GitHub OIDC on each cloud, operational facts and versions.
- [Research 18 §3.2](../18-cloud-blob-storage.md#32-cloud-native-replication): native
  replication and the mirror.
- Decisions [0027](0027-cloud-blob-backends.md), [0028](0028-blob-mirroring.md) and
  [0029](0029-cdn-delivery-and-download-redirect.md).
- [Scenario README template](../../../deploy/platform/SCENARIO-TEMPLATE.md),
  [module README template](../../../deploy/platform/MODULE-TEMPLATE.md) and
  [app-layer README template](../../../deploy/artifactweave/TEMPLATE.md).
- [Definition of done](../../implementation/definition-of-done.md) and
  [deviations register](../../implementation/deviations.md).
