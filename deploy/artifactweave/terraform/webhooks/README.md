# webhooks: webhook subscription administration

This reference configures exactly 24 webhook subscriptions in a running
ArtifactWeave, using `jfrog/artifactory` 12.11.14. Use it to manage subscription
configuration for twelve event domains with standard and custom handlers. It
creates no cloud resources and qualifies **subscription administration only**.
Storing a receiver, event type or future domain makes no delivery or signing
assertion.

## What it configures

Each domain has one standard resource (`artifactory_<domain>_webhook`) and one
custom resource (`artifactory_<domain>_custom_webhook`):

- `artifact`: artifact deployment subscriptions.
- `artifact_property`: artifact property subscriptions.
- `artifact_lifecycle`: artifact lifecycle subscriptions.
- `docker`: Docker image subscriptions.
- `build`: build subscriptions.
- `release_bundle`: release bundle subscriptions.
- `distribution`: distribution subscriptions.
- `artifactory_release_bundle`: Artifactory release bundle subscriptions.
- `destination`: destination subscriptions.
- `user`: user subscriptions.
- `release_bundle_v2`: release bundle v2 subscriptions.
- `release_bundle_v2_promotion`: release bundle v2 promotion subscriptions.

The exact required inventory is in
[webhook-cases.json](../../../../test/terraform/webhook-cases.json), derived from
the [pinned provider source](https://github.com/jfrog/terraform-provider-artifactory/tree/v12.11.14/pkg/artifactory/resource/webhook).
The pinned provider deprecates both `artifactory_release_bundle` domain resources
in favour of `destination`; both remain required here to qualify the planned API.
The subscriptions use fixed `tf-webhook-...` keys, so use a separate instance or
check for existing subscriptions with those keys before applying.

## Prerequisites

- A running ArtifactWeave and its base URL, without `/artifactory`.
- A platform administrator access token for subscription administration.
- Terraform 1.17 or later, which installs `jfrog/artifactory` 12.11.14 as pinned in
  `versions.tf` and `.terraform.lock.hcl`.
- A receiver URL to store in the handler configurations. This reference does not
  contact the receiver.

## Inputs to set

Set the non-secret inputs in `terraform.tfvars`:

```hcl
url          = "https://artifacts.example.com"
receiver_url = "https://receiver.example.com/webhooks"
revision     = 1
```

Supply the sensitive inputs through `TF_VAR_access_token` and
`TF_VAR_handler_secret` environment variables. `revision` defaults to `1`; setting
it to `2` changes handler URLs, headers, custom methods/payloads, signing flags,
and applicable criteria.

## Applying it

With the inputs above and sensitive environment variables set, run:

```sh
terraform init
terraform apply
terraform plan -detailed-exitcode
```

The plan must exit with code 0, showing no changes after refresh. Then set
`revision = 2`, rotate `TF_VAR_handler_secret`, apply again, and require another
empty plan. Each apply creates or updates all 24 subscriptions.

The pinned promotion resource sends only `selectedEnvironments`; the fixture
therefore exercises that selector. Its shared schema exposes pattern attributes
which its request conversion omits. The RBv2 resource sends selected names under
the `release-bundles-v2` map key. The direct API assertions use those actual
[promotion](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/webhook/resource_artifactory_webhook_release_bundle_v2_promotion.go)
and [RBv2](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/webhook/resource_artifactory_webhook_release_bundle_v2.go)
conversions, with no delivery inference.

## Security

API reads redact secrets. ArtifactWeave seals them in its database. Terraform
marks the access token and handler secret sensitive but retains sensitive values
in local state, so keep that state private. Pass credentials through environment
variables and protect the environment as well.

The provider's standard/custom resources do not expose project scope, so these
subscriptions are global. This configuration grants no user or group permissions.

## Teardown

```sh
terraform destroy
```

Destroy deletes all 24 subscriptions. It leaves the running ArtifactWeave and the
receiver unchanged. Protect any local state or backup files that remain because
they can retain sensitive values.

## How this scenario is tested

`make terraform TERRAFORM_MODE=apply` runs `test/terraform/run.sh` against a fresh
`-cover` ArtifactWeave in the required provider lane. It passes the server URL and
an administrator token, `receiver_url = <server URL>/controlled-webhook-receiver`,
`revision = 1` and the fixture-only `handler_secret = Terraform-webhook-secret-1`.
The receiver uses a controlled loopback path and receives no test traffic.

The driver checks all creates, an empty plan, direct GET/list field and redaction
checks, then applies `revision = 2` with the fixture-only
`handler_secret = Terraform-webhook-secret-2`. It requires another empty plan,
checks the changed handler and criteria fields and verifies that persisted
application database bytes do not contain the plaintext fixture secrets. Separate
HTTP controls check project isolation and immutable ownership, including rejected
foreign reads/writes/deletes and unchanged state after malformed URL/template
updates. All deletes are observed as GET 404 and absent from the collection.
Missing or duplicate inventory cases fail before provider installation; a missing
provider fails init. Coverage joins the existing 95% total / 90% per-package CI
gate.

For local diagnosis, `TERRAFORM_PROVIDER_MIRROR` may name an existing filesystem
mirror; committed lock checksums and provider versions still apply. CI uses the
default SHA-256-checked release mirror. `TERRAFORM_COVERPKG` may scope local build
instrumentation; CI retains whole-module instrumentation by default.

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.17 |
| artifactory | 12.11.14 |

### Providers

| Name | Version |
| ---- | ------- |
| artifactory | 12.11.14 |

### Modules

No modules.

### Resources

| Name | Type |
| ---- | ---- |
| [artifactory_artifact_custom_webhook.artifact_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/artifact_custom_webhook) | resource |
| [artifactory_artifact_lifecycle_custom_webhook.artifact_lifecycle_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/artifact_lifecycle_custom_webhook) | resource |
| [artifactory_artifact_lifecycle_webhook.artifact_lifecycle_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/artifact_lifecycle_webhook) | resource |
| [artifactory_artifact_property_custom_webhook.artifact_property_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/artifact_property_custom_webhook) | resource |
| [artifactory_artifact_property_webhook.artifact_property_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/artifact_property_webhook) | resource |
| [artifactory_artifact_webhook.artifact_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/artifact_webhook) | resource |
| [artifactory_artifactory_release_bundle_custom_webhook.artifactory_release_bundle_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/artifactory_release_bundle_custom_webhook) | resource |
| [artifactory_artifactory_release_bundle_webhook.artifactory_release_bundle_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/artifactory_release_bundle_webhook) | resource |
| [artifactory_build_custom_webhook.build_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/build_custom_webhook) | resource |
| [artifactory_build_webhook.build_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/build_webhook) | resource |
| [artifactory_destination_custom_webhook.destination_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/destination_custom_webhook) | resource |
| [artifactory_destination_webhook.destination_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/destination_webhook) | resource |
| [artifactory_distribution_custom_webhook.distribution_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/distribution_custom_webhook) | resource |
| [artifactory_distribution_webhook.distribution_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/distribution_webhook) | resource |
| [artifactory_docker_custom_webhook.docker_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/docker_custom_webhook) | resource |
| [artifactory_docker_webhook.docker_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/docker_webhook) | resource |
| [artifactory_release_bundle_custom_webhook.release_bundle_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/release_bundle_custom_webhook) | resource |
| [artifactory_release_bundle_v2_custom_webhook.release_bundle_v2_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/release_bundle_v2_custom_webhook) | resource |
| [artifactory_release_bundle_v2_promotion_custom_webhook.release_bundle_v2_promotion_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/release_bundle_v2_promotion_custom_webhook) | resource |
| [artifactory_release_bundle_v2_promotion_webhook.release_bundle_v2_promotion_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/release_bundle_v2_promotion_webhook) | resource |
| [artifactory_release_bundle_v2_webhook.release_bundle_v2_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/release_bundle_v2_webhook) | resource |
| [artifactory_release_bundle_webhook.release_bundle_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/release_bundle_webhook) | resource |
| [artifactory_user_custom_webhook.user_custom](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/user_custom_webhook) | resource |
| [artifactory_user_webhook.user_standard](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/user_webhook) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| access\_token | Platform administrator access token used to manage subscriptions. | `string` | n/a | yes |
| handler\_secret | Write-only fixture secret for standard and custom handlers. | `string` | n/a | yes |
| receiver\_url | Webhook receiver URL stored in each handler; this fixture qualifies administration only. | `string` | n/a | yes |
| revision | Configuration revision used to exercise handler and criteria updates. | `number` | `1` | no |
| url | ArtifactWeave platform URL. | `string` | n/a | yes |

### Outputs

| Name | Description |
| ---- | ----------- |
| webhook\_keys | The 24 required subscriptions, indexed by exact provider resource type. |
<!-- END_TF_DOCS -->
