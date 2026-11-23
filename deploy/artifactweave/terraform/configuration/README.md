# configuration: proxy, properties, backup, mail and layout

Manage an outbound proxy, an assignable property set, a backup definition,
the mail server settings and a repository layout with the pinned JFrog
provider. It creates no repositories or cloud resources.

## What it configures

- `artifactory_proxy.this`: a named outbound proxy with optional authentication,
  default-service selection and redirect hosts.
- `artifactory_property_set.this`: named properties, selection flags and
  predefined values with explicit default choices.
- `artifactory_backup.this`: a backup definition with its Quartz schedule,
  retention, flags and excluded repositories. artifactweave stores and exports
  the definition but does not yet run backups (P36), so applying it writes
  nothing outside the server's database.
- `artifactory_mail_server.this`: the SMTP server, sender, subject prefix,
  linked URL and TLS mode used for every mail the server sends.
- `artifactory_repository_layout.this`: a named layout, the path patterns and
  integration revision expressions by which repositories that use it read an
  artifact's organisation, module and version. Setting
  `layout_descriptor_path_pattern` turns on a distinct descriptor pattern.

## Prerequisites

- A running artifactweave or Artifactory with YAML descriptor PATCH and XML
  descriptor GET available, and its base URL without `/artifactory`.
- An admin access token.
- Terraform 1.17 or later and `jfrog/artifactory` 12.11.14, as pinned in
  `versions.tf` and `.terraform.lock.hcl`.

## Inputs to set

```hcl
url               = "https://artifacts.example.com"
proxy_key         = "outbound-proxy"
proxy_host        = "proxy.example.com"
proxy_port        = 3128
proxy_username    = "proxy-user"
property_set_name = "release-properties"
backup_cron_exp   = "0 0 2 ? * MON-FRI"
mail_host         = "smtp.example.com"
mail_port         = 587
mail_use_tls      = true
mail_from         = "artifacts@example.com"
mail_artifactory_url = "https://artifacts.example.com/artifactory/"
layout_name       = "release-layout"
layout_artifact_path_pattern = "[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"
```

Pass credentials through sensitive environment variables such as
`TF_VAR_access_token`, `TF_VAR_proxy_password` and `TF_VAR_mail_password`.
`backup_excluded_repositories` must name repositories that already exist. Set
`properties` to replace
the default release/free definitions. Each predefined-value map entry is a
boolean indicating whether that value is selected by default.

## Applying it

```sh
terraform init
terraform apply
terraform plan
```

The follow-up plan is empty after the provider refreshes the XML descriptor.
Changing the proxy or mail password updates the server secret; setting it to
null clears it. Changing `mail_host` replaces the mail settings, and changing
`layout_name` replaces the layout. A repository uses the layout once its
`repoLayoutRef` names it. Run
`artifactweave mail test <address>` on the server host to send a test message
through the stored settings.

## Security

The access token, proxy password and mail password are sensitive inputs and remain out of
ordinary plans and logs. Terraform state can retain sensitive values, so keep
it private. artifactweave seals the proxy password at rest when a master key is
configured, and never returns either password. Only include trusted hosts in `proxy_redirect_to_hosts`, since
proxy authentication may be sent to those hosts.

## Teardown

`terraform destroy` removes all five. Removing the mail server settings
disables every mail the server sends. Remove repository references to the
proxy, property set and layout before destroying them.

## How this scenario is tested

The existing required Terraform conformance lane runs this reference against
its own temporary server through `test/terraform/run.sh`. The driver supplies
two revisions of ordinary inputs, applies and refreshes each and requires
empty plans. After each, `test/terraform/check-p12.py` reads all five
resources back from the XML descriptor and compares every field with the
revision's inputs. For the layout it also asks for the effect: a generic
repository on the layout holds `acme/tool/1.0/tool-1.0.bin` and
`releases/acme/tool/2.0/tool-2.0.bin`, and the version search for `acme:tool`
must find 1.0 under revision 1's pattern and 2.0 under revision 2's. The
driver then tests omitted-password retention through a partial descriptor
patch, clears both passwords, changes both ports so the provider sends the
empty passwords again, destroys all five resources and checks that they are
gone from the descriptor and that no backup directory appeared. A read-only
checker uses the server's own keyring/store code to check the actual secret
and sealed rows after each phase; retained evidence contains hashes and
assertion results.

The provider reads every resource back through the XML descriptor GET (#1825),
so a server without it fails these controls rather than skipping them.

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
| [artifactory_backup.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/backup) | resource |
| [artifactory_mail_server.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/mail_server) | resource |
| [artifactory_property_set.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/property_set) | resource |
| [artifactory_proxy.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/proxy) | resource |
| [artifactory_repository_layout.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/repository_layout) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| access\_token | Admin access token, sent as a Bearer token. | `string` | n/a | yes |
| backup\_create\_archive | Whether a backup is written as a Zip archive. | `bool` | `false` | no |
| backup\_cron\_exp | Quartz cron expression of the backup schedule. | `string` | `"0 0 2 ? * MON-FRI"` | no |
| backup\_enabled | Whether the backup definition is enabled. | `bool` | `true` | no |
| backup\_exclude\_new\_repositories | Whether repositories created later are left out of the backup. | `bool` | `false` | no |
| backup\_excluded\_repositories | Existing repositories to leave out of the backup; null excludes none. Keep it non-empty once set: the pinned provider omits an empty list, which leaves the stored one in place. | `list(string)` | `null` | no |
| backup\_export\_mission\_control | The exportMissionControl flag of the backup definition. | `bool` | `false` | no |
| backup\_key | Unique key of the backup definition: an XML name. | `string` | `"nightly"` | no |
| backup\_retention\_period\_hours | Hours to keep a backup; 0 keeps every backup. | `number` | `168` | no |
| backup\_send\_mail\_on\_error | Whether administrators are mailed when a backup fails. | `bool` | `true` | no |
| backup\_verify\_disk\_space | Whether a backup first checks the target has room (precalculate). | `bool` | `false` | no |
| layout\_artifact\_path\_pattern | Path pattern of the layout's artifacts, in layout tokens such as [org], [module], [baseRev] and [ext]. | `string` | `"[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]"` | no |
| layout\_descriptor\_path\_pattern | Path pattern of the layout's descriptors (such as a POM); null uses the artifact path pattern. | `string` | `null` | no |
| layout\_file\_integration\_revision\_regexp | Regular expression of the integration revision in a file name, without capturing groups; .* when not applicable. | `string` | `"SNAPSHOT|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))"` | no |
| layout\_folder\_integration\_revision\_regexp | Regular expression of the integration revision in a folder name, without capturing groups; .* when not applicable. | `string` | `"SNAPSHOT"` | no |
| layout\_name | Name of the repository layout: an XML name, unique among descriptor keys. Changing it replaces the layout. | `string` | `"release-layout"` | no |
| mail\_artifactory\_url | Registry URL linked from outgoing mail. Required here: the pinned provider reads an unset URL back as an empty string and plans a change. | `string` | n/a | yes |
| mail\_enabled | Whether mail notifications are sent. | `bool` | `true` | no |
| mail\_from | Sender address of outgoing mail. | `string` | `null` | no |
| mail\_host | SMTP server host name or IP address. Changing it replaces the mail server settings. | `string` | n/a | yes |
| mail\_password | SMTP AUTH password; null clears it. Sealed by the server and never returned; Terraform retains this sensitive attribute in private state. | `string` | `null` | no |
| mail\_port | SMTP server port. | `number` | `25` | no |
| mail\_subject\_prefix | Prefix of every outgoing subject. Set a value: a null prefix reads back as the [JFrog] default and plans a change. | `string` | `"[artifactweave]"` | no |
| mail\_use\_ssl | Whether to use implicit TLS from the first byte. | `bool` | `false` | no |
| mail\_use\_tls | Whether to upgrade the connection with STARTTLS. | `bool` | `false` | no |
| mail\_username | SMTP AUTH username; null sends no credentials. | `string` | `null` | no |
| properties | Property names, selection flags and predefined values mapped to whether each is selected by default. | ```map(object({ closed_predefined_values = optional(bool, false) multiple_choice = optional(bool, false) predefined_values = optional(map(bool), {}) }))``` | ```{ "free": {}, "release": { "closed_predefined_values": true, "predefined_values": { "ready": true, "waiting": false } } }``` | no |
| property\_set\_name | Name of the property set. | `string` | `"release-properties"` | no |
| property\_set\_visible | Whether the property set is visible and assignable. | `bool` | `true` | no |
| proxy\_host | Hostname or IP address of the outbound proxy. | `string` | n/a | yes |
| proxy\_key | Unique key of the outbound proxy. | `string` | `"outbound-proxy"` | no |
| proxy\_password | Password for authenticated proxy connections; null clears it. Terraform retains this sensitive attribute in private state. | `string` | `null` | no |
| proxy\_platform\_default | Whether this proxy is the platform default. Keep proxy\_services empty when true. | `bool` | `false` | no |
| proxy\_port | Port of the outbound proxy. | `number` | `3128` | no |
| proxy\_redirect\_to\_hosts | Hosts to which the proxy may redirect requests while retaining its credentials. | `set(string)` | `[]` | no |
| proxy\_services | Services using this default proxy: jfrt, jfmc, jfxr or jfds. | `set(string)` | `[]` | no |
| proxy\_username | Username for authenticated proxy connections. | `string` | `null` | no |
| url | Base URL of artifactweave, without /artifactory. | `string` | n/a | yes |

### Outputs

| Name | Description |
| ---- | ----------- |
| backup\_key | Key of the configured backup definition. |
| layout\_name | Name of the configured repository layout. |
| mail\_host | SMTP host of the configured mail server. |
| property\_set\_name | Name of the configured property set. |
| proxy\_key | Key of the configured outbound proxy. |
<!-- END_TF_DOCS -->
