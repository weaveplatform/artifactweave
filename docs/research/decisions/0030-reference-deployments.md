# 0030: Reference deployments and platform testing

Status: Proposed

## Context

artifactweave supports fs, S3, GCS and Azure blob stores, a quorum mirror across them, and
(decision [0029](0029-cdn-delivery-and-download-redirect.md)) CDN delivery on all three
clouds. Operators need a tested way to stand each combination up, and the project needs to
prove each combination works against real cloud services, not only fakes and emulators.

Today the repository has two infrastructure Terraform roots, for the GCP and Azure
acceptance environments, plus the JFrog provider fixture in `test/terraform` that
configures artifactweave itself. CI checks none of the infrastructure roots, there is no
AWS Terraform, and each acceptance root mixes its bucket with long-lived CI trust
([research 20](../20-reference-deployments.md) section 1).

The maintainer decided on 2026-10-14:
- every Terraform root the Makefile uses lives under `deploy/`;
- infrastructure goes in `deploy/platform/terraform/<cloud>/<scenario>`, and app
  configuration through the JFrog providers in `deploy/artifactweave/terraform/<scenario>`;
- real-cloud runs, teardown included, are dispatched by hand;
- the Azure CDN scenario uses Front Door Premium with Private Link;
- the DNS subdomain is `artifact.weaveplatform.run`;
- the cloud accounts are labs.

## Decision

**Layout.**

```text
deploy/
  platform/
    README.md                         scenario catalogue, costs, how CI runs them
    terraform/
      scenario.mk                     shared make targets for every scenario
      bootstrap.mk                    shared make targets for the bootstraps
      github-environments.sh          creates the cloud-conformance and cloud-platform environments
      modules/<cloud>/<capability>    child modules: storage, cdn, ci-access; no provider blocks
      aws/ gcp/ azure/
        bootstrap/                    long-lived: CI identities, remote state, DNS zone, APIs, budget
        single-bucket/                one private bucket or container
        multi-region/                 two regional stores as artifactweave mirror members
        geo-replicated/               one store with the cloud's own replication
        cdn/                          a store behind CloudFront, Cloud CDN or Front Door
      multicloud/
        mirror/                       one mirror with a member on each cloud
  artifactweave/
    README.md                         managing artifactweave with the JFrog Terraform providers
    terraform/<scenario>/             repositories, identity, cdn-repositories, ...
test/terraform/run.sh                 the driver: builds a -cover server, mints a token, applies a
                                      deploy/artifactweave scenario, requires an empty plan, destroys
```

- `bootstrap` is not a scenario: an operator applies it once per cloud and it outlives every
  scenario. It holds the data-plane and deployer identities, so a scenario such as
  `single-bucket` only looks its identity up and grants it the scenario's store.
  `make destroy` refuses on a bootstrap.
- Child modules are per cloud. There is no cross-cloud abstraction module: each cloud's
  storage, CDN and identity resources differ too much for one interface to help.
- Compute hosting of artifactweave itself, when it is added, is a further `deploy/platform`
  scenario.
- `test/terraform` keeps only its driver. The provider fixture becomes the
  `deploy/artifactweave/terraform/repositories` reference.

**The scenario contract.** Every scenario root has:
- a README whose inputs and outputs terraform-docs injects between `BEGIN_TF_DOCS` markers,
  with a cost note and security notes;
- an `artifactweave_config` output rendering the `blob` section of the `artifactweave.yaml`
  the scenario needs, so the reference shows the exact server configuration, plus a
  `storage_host` output (where a `downloadRedirect` repository's signed URLs point) and an
  `expires_at` output (the instance's `expires-at` label, which `reap` reads from state);
- a `ci-vars.sh` that sets the Terraform variables a CI run passes, because the deployer
  applies in CI where an operator applies on a workstation (for `single-bucket`: the
  deployer as the identity artifactweave runs as, Azure's scenarios resource group, no
  provider registration, the instance's labels);
- `tests/*.tftest.hcl` with `mock_provider`, asserting the scenario's security invariants
  (examples: public access blocked; CloudFront origin access control; `cloud-cdn-fill`
  limited to read; Front Door `UseQueryString` and a cache-expiry cap; empty origin paths);
- labels `artifactweave-scenario` and `expires-at` (epoch seconds) on every resource that
  takes labels, and a name prefix with the scenario and instance on every resource, so
  resources without labels are still found;
- no dependence on directory depth: `scenario.mk` finds the repository root with
  `git rev-parse --show-toplevel`, and compose files carry an explicit `name:`.

**Identities and environments.**

| GitHub environment | Trusted by | Rights | Used by |
|---|---|---|---|
| `cloud-conformance` | a data-plane identity in each cloud | objects in the conformance bucket or container only | `cloud-conformance.yml` |
| `cloud-platform` | a deployer identity in each cloud | create and destroy scenario resources in the lab project, subscription and account | `platform-scenarios.yml` |

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
  - AWS: buckets with the scenario prefix (`artifactweave-*`), CloudFront and ACM, records
    in the scenarios' hosted zone, and roles under `/artifactweave-scenarios/` only. A
    permissions boundary caps the deployer and must be attached to every role it creates,
    so it cannot grant more than it holds; the boundary also keeps it out of the
    bootstrap's state, roles, policies and OIDC provider.
  - AWS's data-plane role holds no policy: the `single-bucket` bucket policy grants it that
    bucket, so its rights leave with the bucket.
- Bootstrap enables the APIs and resource providers scenarios need, so a deployer never
  needs service-usage administration.

**State.**
- Bootstrap creates a private, versioned remote state bucket or container per cloud: GCS,
  Azure Storage, and S3 with `use_lockfile`.
- Scenario state lives at `scenarios/<cloud>/<scenario>/<instance>`, so an `apply` dispatch
  and a later `destroy` dispatch find the same state.
- A bootstrap starts on local state, because its backend does not exist yet, and moves its
  state into the bucket it created with `terraform init -migrate-state`, so no single
  machine holds it. Its committed backend block names the store, so the store's name is
  fixed rather than generated (`artifactweave-tfstate-<project number>` on GCP,
  `awtfstate<subscription prefix>` on Azure, `artifactweave-tfstate-<fixed random suffix>`
  on AWS, so the account ID stays out of the repository); while the store does not exist,
  `make init` writes a git-ignored `local_override.tf` that replaces the backend with local
  state.
- State and plan files are never uploaded as workflow artifacts.

**Keys.**
- CDN signing keys are generated in the workflow job, outside Terraform, and handed to
  artifactweave through a file. Only the CloudFront public key reaches Terraform.
- The Cloud CDN key is added with `gcloud` after apply, because
  `google_compute_backend_bucket_signed_url_key` stores its `key_value` in state as plain
  text and has no write-only variant. If the provider gains one, the scenario switches to it.

**DNS.**
- Each cloud's bootstrap creates a zone under `artifact.weaveplatform.run`:
  `aws.artifact.weaveplatform.run` (Route 53), `gcp.artifact.weaveplatform.run` (Cloud DNS)
  and `azure.artifact.weaveplatform.run` (Azure DNS). `weaveplatform.run` is served by
  GoDaddy, where the maintainer adds each zone's NS records from the bootstrap's output.
  The three zones were created by CLI before their bootstraps, so the delegation could be
  set up first, and each bootstrap imports its zone; Terraform refuses to destroy them.
- The AWS roots recognise the lab account by its hosted zone (`aws/check-account.sh`)
  rather than an account ID, which this public repository does not record.
- A CDN host is a sibling of artifactweave's host, never a subdomain of it (for example
  `registry.gcp.artifact.weaveplatform.run` and `cdn.gcp.artifact.weaveplatform.run`), so
  clients drop credentials on the redirect (decision 0029).

**Checks on every pull request.** A `deploy-terraform` job, required by the gate, runs over
`deploy/` and `test/terraform`:
- `terraform fmt -check -recursive`;
- `terraform validate` per root with `init -backend=false`;
- tflint per root with the aws, google and azurerm rulesets pinned in `.tflint.hcl`;
- `trivy config` per root, with each accepted finding a `#trivy:ignore` comment beside the
  resource stating its reason;
- `terraform test` for every root with `tests/`, using mocked providers only;
- a terraform-docs drift check: regenerate each README in a copy and fail when it differs
  from the committed file.

`deploy/check.sh` runs them (`make deploy-check` locally). It installs terraform, tflint,
trivy and terraform-docs by exact version, each archive checked against the SHA-256 from
its release, rather than through third-party actions. It walks the roots it finds, not
`--recursive` from the repository root, which would also descend into worktrees and tool
caches.

**Real-cloud runs.** `platform-scenarios.yml` runs on `workflow_dispatch`, under the
`cloud-platform` environment. Its inputs are `cloud`, `scenario`, `instance`, an `action`
and `ttl_minutes` (how long until an applied instance's `expires-at`):

| Action | Does |
|---|---|
| `test` | apply with a unique instance and `expires-at`, start artifactweave from the rendered configuration, run the `test/platform` suite, destroy under `always()` |
| `apply` | apply and leave the instance running, for exploration |
| `destroy` | destroy a named instance |
| `reap` | destroy every instance whose `expires-at` has passed, finding resources by label or name prefix |

- `concurrency` is keyed on cloud, scenario and instance, with `cancel-in-progress: false`,
  so an apply is never cut off halfway.
- One driver, `deploy/platform/terraform/scenario-ci.sh`, runs every action. Each action
  works on a fresh copy of the scenario with a generated backend for the instance's state,
  so nothing carries over between invocations except that state, and a destroy finds what a
  separate apply left exactly as a later dispatch does. A destroy deletes the emptied state.
- `reap` reads each state's `expires_at` output and destroys the expired instances, each in
  its own process so a failed destroy never goes on to delete its state. It then deletes
  resources that outlived their state: those labelled with a passed `expires-at`, and
  unlabelled ones carrying the `awscn-` name prefix once they are two hours old and belong
  to no live instance, so a run still applying keeps its resources.
- A dispatch only finds workflows on the default branch, so a pull request that changes the
  workflow, the driver, `test/platform` or a `single-bucket` scenario runs a proof instead,
  behind the same approval: for each cloud, `test`, an `apply` followed by a `destroy` from
  the remote state alone, an instance applied already expired and a `reap` that removes it,
  then a check that nothing of the run remains. Pull requests from forks get no OIDC token
  and do not run. A cloud joins that proof once a dispatched run has proven its wiring.
- On AWS the deployer may only manage buckets named `artifactweave-*`, so a scenario run
  names its bucket `artifactweave-awscn-<instance>-…`, and reap finds AWS buckets by their
  tags through the tagging API rather than by the `awscn-` prefix.
- `test/platform` is a Go suite of `_test.go` files only, gated on `ARTIFACTWEAVE_SCENARIO`
  like the real-cloud blob tests. Scenario runs are evidence that a deployment works, not
  coverage: they upload no coverage profiles and the gate never waits for them.

**Dependencies.** Dependabot's `terraform` ecosystem covers `deploy/platform` with major
versions ignored, since a provider major merged on mocked tests alone could break a real
apply. `deploy/artifactweave` is excluded: `test/terraform/run.sh` pins its `jfrog/artifactory`
and `jfrog/platform` provider zips by SHA-256, which Dependabot cannot update. `deps-refresh`
does not touch Terraform providers either, so the version, the checksums and the lock file
move together by hand, following the steps in
[docs/testing/ci.md](../../testing/ci.md#terraform-scenarios).

**Lab accounts.** The accounts behind these environments are labs. The design asks for no
operator-hardening steps, such as separate non-root administrators or MFA ceremonies. The
scenarios themselves still demonstrate private storage, OIDC trust without stored secrets
and least-privilege CI roles, because those are part of what a reference shows.

## Rationale

- **One environment per job decides the environment set.** A job references exactly one
  environment ([research 20](../20-reference-deployments.md) section 4.1). Per-cloud
  environments would leave the multicloud scenario unable to authenticate to all three
  clouds in one job. Splitting data plane from deployer keeps the conformance tests on
  object-level rights while scenario runs get the broader rights they need, behind an
  approval.
- **Remote state follows from manual teardown.** With teardown dispatched separately from
  apply, state must outlive the job that created it. Local state in the runner would strand
  every `apply` instance.
- **Bootstrap is split from scenarios** so scenarios can be created and destroyed freely
  without touching the trust that CI depends on. A destroyed GCP workload identity pool's ID
  stays reserved for about 30 days (section 4.3), so recreating trust with each scenario is
  not an option.
- **Keys stay out of state** because state is readable by anyone who can read the backend,
  and two of the obvious resources (`tls_private_key`, the Cloud CDN signed URL key) store
  key material there in plain text (section 5).
- **Manual dispatch over a schedule** was the maintainer's choice: CDN scenarios carry fixed
  hourly costs (Front Door Premium's base fee, a Cloud load balancer's forwarding rules), and
  a scheduled run would spend them without anyone looking at the result.
- **Mocked tests on every pull request** catch the security invariants that matter most in
  a reference (public access, query-string caching, origin access) without credentials, so
  forks and Dependabot pull requests get them too.
- **Scenario runs are not coverage** because they are dispatched by hand and depend on cloud
  state; feeding them into the gate would make it non-deterministic.
- **Alternatives rejected:**
  - one generic multi-cloud storage module: the resources differ too much, and a reference
    should read like the cloud's own idiom;
  - keeping `test/terraform` as the app-layer home: it is a test asset, while operators
    need references they can copy;
  - the trust inside each scenario root: see the pool ID reservation above.
- **Deviation:** none from Artifactory. JFrog publishes binary-provider templates per cloud,
  not tested deployments; this decision adds deployments around the same storage choices.

## Constraints

- No emulator exists for CloudFront, Cloud CDN or Front Door, so CDN behaviour is proven
  only by real-cloud runs; acceptance uses a fake edge (decision 0029).
- GCP label values allow only lowercase letters, digits, underscores and dashes, so
  `expires-at` holds epoch seconds. Backend buckets, URL maps and target proxies take no
  labels in the Terraform provider, and Compute documents forwarding-rule labels only for
  internal load balancers, so the reaper also matches name prefixes (section 5).
- An Azure identity holds at most 20 federated credentials and no wildcards, so each
  environment needs its own exact credential (section 4.4).
- Required reviewers on environments are available to this repository only because it is
  public (section 4.1).
- Mocked tests need Terraform 1.7 or later, write-only arguments 1.11 or later (section 3).
- CloudFront distributions, Front Door profiles and managed certificates take minutes to
  tens of minutes to create and delete, which bounds how fast a `test` run can be.
- The GCP acceptance pool `github-actions` was deleted on 2026-10-14; until about
  2026-11-13 its ID can only be reused by undeleting it. `gcp/bootstrap` undeletes it
  (`make undelete-pool`) and imports it, with its provider, rather than choosing a new ID.
- The DNS zones were created by CLI before the bootstraps, so their delegation at GoDaddy
  could be set up; the bootstraps import them and refuse to destroy them, because a new zone
  would get new name servers.

## Verification

- The `deploy-terraform` job passes on every pull request and fails on an unformatted file,
  a lint finding, an unrecorded scanner finding, a failing mocked test or a stale README.
- The mocked tests in each scenario assert its security invariants.
- Each scenario has a recorded `platform-scenarios` `test` run that applied, passed
  `test/platform` and destroyed, linked from its pull request.
- `make terraform TERRAFORM_MODE=apply` applies each `deploy/artifactweave` scenario through
  `test/terraform/run.sh` with an empty plan before destroy.

## References

- [Research 20](../20-reference-deployments.md): multi-region storage, Terraform practice,
  GitHub OIDC on each cloud, operational facts and versions.
- [Research 18 §3.2](../18-cloud-blob-storage.md#32-cloud-native-replication): native
  replication and the mirror.
- Decisions [0027](0027-cloud-blob-backends.md), [0028](0028-blob-mirroring.md) and
  [0029](0029-cdn-delivery-and-download-redirect.md).
- [Definition of done](../../implementation/definition-of-done.md) and
  [deviations register](../../implementation/deviations.md).
