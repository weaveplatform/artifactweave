# Webhook subscription administration

This reference contains exactly 24 resources: twelve event domains with standard
and custom handlers, using `jfrog/artifactory` 12.11.14. The required inventory is
in [webhook-cases.json](../../../../test/terraform/webhook-cases.json), derived from
the [pinned provider source](https://github.com/jfrog/terraform-provider-artifactory/tree/v12.11.14/pkg/artifactory/resource/webhook).

Supply `url`, a platform administrator `access_token`, `receiver_url`, and
`handler_secret` through `TF_VAR_…` environment variables. Apply revision 1,
require an empty `terraform plan -detailed-exitcode`, then set `revision=2` and
rotate `handler_secret`. Apply and require an empty plan again. Revision 2 changes
handler URLs, headers, custom methods/payloads, signing flags, and applicable
criteria. `terraform destroy` deletes all 24 subscriptions.

The pinned promotion resource sends only `selectedEnvironments`; the fixture
therefore exercises that selector. Its shared schema exposes pattern attributes
which its request conversion omits. The RBv2 resource sends selected names under
the `release-bundles-v2` map key. The direct API assertions use those actual
[promotion](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/webhook/resource_artifactory_webhook_release_bundle_v2_promotion.go)
and [RBv2](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/resource/webhook/resource_artifactory_webhook_release_bundle_v2.go)
conversions, with no delivery inference.

The provider's standard/custom resources do not expose project scope. Project
isolation and immutable ownership are checked separately through HTTP, including
rejected foreign reads/writes/deletes and malformed URL/template updates.

This scenario qualifies **subscription administration only**. Storing a receiver,
event type or future domain makes no delivery or signing assertion. The receiver
uses a controlled loopback path in the test runner and receives no test traffic.
The pinned provider deprecates both `artifactory_release_bundle` domain resources
in favour of `destination`; both remain required here to qualify the planned API.

API reads redact secrets. ArtifactWeave seals them in its database. Terraform
marks the supplied secret sensitive but retains it in local state, so keep that
state private. The fixture passes credentials through environment variables and
its checker reports only case names and field/status outcomes.

`make terraform TERRAFORM_MODE=apply` runs this scenario in the required provider
lane: all creates, empty plans, direct GET/list field and redaction checks, handler
and secret updates, another empty plan, project/validation controls, and all
deletes observed as GET 404 and absent from the collection. Missing or duplicate
inventory cases fail before provider installation; a missing provider fails init.
Coverage joins the existing 95% total / 90% per-package CI gate.

For local diagnosis, `TERRAFORM_PROVIDER_MIRROR` may name an existing filesystem
mirror; committed lock checksums and provider versions still apply. CI uses the
default SHA-256-checked release mirror. `TERRAFORM_COVERPKG` may scope local build
instrumentation; CI retains whole-module instrumentation by default.

## Reference

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.6 |
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
