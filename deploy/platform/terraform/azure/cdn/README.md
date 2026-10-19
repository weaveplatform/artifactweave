# azure/cdn: an Azure store whose downloads Front Door serves over Private Link

A storage account behind an Azure Front Door Premium profile. artifactweave writes to the
account and answers a download from a `downloadRedirect` or `cdnRedirect` repository with a
302 to a SAS it signs, moved onto the Front Door host, so the bytes come from the edge nearest
the client. Front Door reaches the account over Private Link, so the account's public endpoint
answers artifactweave's own networks, or nothing at all. Choose it when clients are spread
across regions or download the same artifacts often, and the Premium profile's hourly fee is
worth it. Choose `single-bucket` when clients sit beside the account, or `multi-region` for
copies in several regions that artifactweave writes itself.

## What it deploys

- The account through [`modules/azure/storage`](../../modules/azure/storage/README.md),
  `<up to 15 of name><6 random><3 of location>` in `location`, with its private container. The
  app identity, and each of `additional_principals`, gets Storage Blob Data Contributor on it.
  Its public endpoint takes requests from `app_ip_ranges` and `app_subnet_ids` alone, and is off
  when both are empty.
- The Front Door profile through [`modules/azure/cdn`](../../modules/azure/cdn/README.md): a
  Premium profile and endpoint, `name`, an origin that reaches the account's blob endpoint over
  Private Link, a route that serves the container's committed blobs with the whole query string
  in its cache key, and a rule that keeps an edge copy for `redirect_ttl_minutes`. With
  `domain_name`, also a custom domain with a managed certificate, and its validation TXT record
  and CNAME in `dns_zone_id`.
- `azurerm_resource_group.this`: `<name>-rg`, only when `resource_group_name` is null.
- `random_string.suffix`: six characters that make the account's name globally unique.

## How it behaves

- **Uploads go to the account**, as with `single-bucket`. Front Door only reads.
- **Downloads redirect to the edge.** A GET of a file of at least 1 MiB
  (`download.redirect.min_size`) from a repository with `downloadRedirect` or `cdnRedirect`
  answers 302 to `https://<storage_host>/<container>/<blob>?<SAS>`: a user delegation SAS whose
  start and expiry are fixed to the current window, with the item's filename and media type as
  response overrides and `Cache-Control: public, max-age=<TTL>`, moved from the account's host
  onto Front Door's (decision
  [0029](../../../../../docs/research/decisions/0029-cdn-delivery-and-download-redirect.md)).
  Smaller files, and HEAD and conditional requests, are answered by artifactweave itself, so the
  JFrog CLI's HEAD before a split download works. Each ranged GET of a split download is
  redirected too.
- **Front Door checks nothing; storage checks the SAS.** The edge forwards the request, query
  string included, to the account over Private Link, and the account refuses a request without a
  valid SAS. Every client in one window gets the same URL for a blob, so the edge's cache, keyed
  on the whole URL, hits.
- **An edge copy lives exactly `redirect_ttl_minutes`.** A URL is valid until the end of its
  window plus the TTL, and an edge that cached it just before then serves it for at most the TTL
  longer: with the default 5 minutes, 15 minutes from minting at the worst. The same input sets
  artifactweave's `download.redirect.ttl`, so the two cannot drift. Front Door may serve an
  expired copy while the origin errors, so the bound is a design target, not a guarantee.
- **The redirect header still works.** `X-JFrog-Download-Redirect-To: None` keeps a download on
  artifactweave, and `S3` sends a `downloadRedirect` repository's download to a SAS on the
  account itself, `origin_host`, which only artifactweave's networks reach.
- **Large downloads need Front Door fully rolled out.** For some minutes after the profile is
  created or changed, a download larger than one of Front Door's 8 MiB chunks can stop after the
  first 8 MiB with a reset stream, even though the edge already answers. Smaller files are
  unaffected. Wait until a download of more than 8 MiB through the edge completes before sending
  traffic; the scenario's own tests do exactly that before they start.
- **Clients drop their credentials on the redirect** because the CDN host is never
  artifactweave's host or a subdomain of it; artifactweave refuses to start otherwise. With
  `domain_name`, choose a sibling of artifactweave's host, such as `cdn.example.com` beside
  `artifacts.example.com`.

## Prerequisites

- Terraform 1.9 or later, and an Azure CLI login (`az login`) that can create storage accounts,
  Front Door profiles and role assignments in the resource group: Contributor and Role Based
  Access Control Administrator on it, or Owner. Creating the group needs Contributor on the
  subscription. With `domain_name`, also DNS Zone Contributor on the zone `dns_zone_id`.
- `Microsoft.Storage` and `Microsoft.Cdn` registered in the subscription. The provider registers
  nothing (`resource_provider_registrations = "none"`); `az provider register --namespace
  Microsoft.Cdn` does it once.
- The identity artifactweave runs as, created elsewhere (a managed identity, or a service
  principal), whose object ID is `app_principal_id`, and the networks it runs in:
  `app_ip_ranges` for public addresses outside the account's region, `app_subnet_ids` for
  subnets with the `Microsoft.Storage` service endpoint. With neither, give artifactweave's
  network a private endpoint to the account yourself.
- **Approving Front Door's Private Link.** Front Door asks the account for a private endpoint
  connection when the origin is created, and its owner must approve it; the azurerm provider
  cannot. Until then the edge answers errors. After `terraform apply`:

  ```bash
  az network private-endpoint-connection list --id "$(terraform output -raw storage_account_id)" \
    --query "[?properties.privateLinkServiceConnectionState.status=='Pending'].id" --output tsv |
    while read -r id; do az network private-endpoint-connection approve --id "$id" --description approved; done
  ```

  The pending connection carries `private_link_request_message`. Front Door then takes some
  minutes to bring the endpoint up everywhere; until it does, the endpoint answers 404.

### Getting the code

Copy this directory together with `modules/azure/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point each module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/azure/storage?ref=v0.1.0" # x-release-please-version
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/azure/cdn?ref=v0.1.0" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
subscription_id  = "00000000-0000-0000-0000-000000000000"
name             = "artifactweave-example"
location         = "uksouth"
app_principal_id = "00000000-0000-0000-0000-00000000000a"
app_ip_ranges    = ["203.0.113.7"]

# Optional
# app_subnet_ids        = ["/subscriptions/.../subnets/artifactweave"]
# redirect_ttl_minutes  = 5
# domain_name           = "cdn.example.com"
# dns_zone_id           = "/subscriptions/.../providers/Microsoft.Network/dnsZones/example.com"
# private_link_location = "uksouth"
# resource_group_name   = "artifactweave-rg"
# additional_principals = { operator = { principal_id = "...", principal_type = "User" } }
# container             = "artifactweave"
# store_prefix          = "artifacts/"
# expiring_prefixes     = { "scratch/" = 1 }
# tags                  = { team = "platform" }
```

```bash
terraform init
terraform apply
```

Then approve the private endpoint connection, as Prerequisites says.

## The artifactweave configuration

`artifactweave_config` renders the `blob` and `download` sections of `artifactweave.yaml`: one
`azure` store, the account and container, with `prefix` only when `store_prefix` is set, a
`cdn.front_door` block naming the Front Door host, and `download.redirect.ttl`, which the edge's
cache cap matches. `credential: default` uses the Azure SDK's default chain, and so the app
identity, and lets the store sign user delegation SAS; the account has no keys. The example
below is the rendered output for the example inputs above, with the random suffix `0a1b2c` and
the endpoint host `artifactweave-example-abcdefgh.z01.azurefd.net`; the `readme_example` test
fails if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: azure
  azure:
    account: artifactweaveex0a1b2cuks
    container: artifactweave
    credential: default
    cdn:
      front_door:
        endpoint: artifactweave-example-abcdefgh.z01.azurefd.net
download:
  redirect:
    ttl: 5m
```
<!-- END_CONFIG_EXAMPLE -->

### Running artifactweave with it

```bash
terraform output -raw artifactweave_config > artifactweave.yaml
export ARTIFACTWEAVE_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: it seals stored secrets
export ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD='choose one'      # read on first start only
artifactweave serve --config artifactweave.yaml --base-url https://artifacts.example.com
```

Run it as `app_principal_id`, a managed identity on its VM or container, or a workload
identity, so the Azure SDK's default chain finds it, from one of the networks the account
answers. Then turn on `downloadRedirect` or `cdnRedirect` on the repositories whose downloads
the edge should serve; [`deploy/artifactweave/terraform/cdn-repositories`](../../../../artifactweave/terraform/cdn-repositories/README.md)
does it with the JFrog provider. Their downloads redirect to `storage_host`. Keep
`download.redirect.ttl` as rendered: a longer TTL than the edge's cap only makes URLs live
longer, but a shorter one lets the edge serve copies past their SAS. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

The account carries the [`modules/azure/storage` invariants](../../modules/azure/storage/README.md#invariants):
no public containers, HTTPS and TLS 1.2 only, Shared Key disabled so every request carries an
Entra ID identity or a user delegation SAS, encryption at rest, and abandoned uploads expired.
This scenario also closes its public endpoint to every network but artifactweave's. The
profile carries the [`modules/azure/cdn` invariants](../../modules/azure/cdn/README.md#invariants):
Premium, Private Link to the account with its certificate checked, HTTPS only at both ends, a
route that serves the committed blobs and nothing else in the account, a cache keyed on the
whole URL, and edge copies capped at the TTL.

The app identity, and each of `additional_principals`, holds Storage Blob Data Contributor on
the account: it can read, write, list and delete blobs and mint user delegation keys, and
cannot change the account's settings, keys or role assignments. Front Door holds no identity
and no rights on the account: every read it forwards is authorised by the SAS in the URL, so
the Front Door host alone opens nothing.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AZU-0012 | `azurerm_storage_account.blobs` (`azure/storage`) | Accepted in the module for scenarios that answer any network; this scenario sets `network_rules` to artifactweave's networks, or turns the public endpoint off. |
| AZU-0057 | `azurerm_storage_account.blobs` (`azure/storage`) | No Storage Analytics logs: the activity log records the management calls. |
| AZU-0058 | `azurerm_storage_account.blobs` (`azure/storage`) | LRS: Front Door's edges hold the copies clients read; `geo-replicated` and `multi-region` keep a second region. |
| AZU-0060 | `azurerm_storage_account.blobs` (`azure/storage`) | Microsoft-managed keys; a customer-managed key would add a Key Vault and its access policies to manage. |
| AZU-0061 | `azurerm_storage_account.blobs` (`azure/storage`) | No infrastructure (double) encryption: service-side encryption already applies. |

## Cost

### Steady state

The Front Door Premium profile has a base fee of $330 a month, about $0.45 an hour, billed for
each hour or part of an hour the profile exists, whether it serves anything or not. Private Link
to the origin is included. On top: data from the edge to clients (roughly $0.083 per GB for the
first 10 TB), from the edge to the origin (roughly $0.02 per GB), and requests (roughly $0.015
per 10,000). The account is hot LRS block blobs, roughly $0.02 per GB-month in the UK regions,
plus operations. A leaked profile costs about $10.80 a day, so destroy test instances promptly.

### Per test run

About $0.45 to $0.90: the profile's base fee for the one or two hours a proof spans, which
dominates. The proof's own storage, requests and transfer come to about a cent. To keep that
down, a proof runs the `test` lifecycle only (apply, `test/platform`, destroy), with one
profile, rather than the three the full lifecycle would create.

## Teardown

`terraform destroy` removes the route, rule set, origin, endpoint and profile, with the private
endpoint connection Front Door made, then the account with every blob in it, its role
assignments and, when this root created it, the resource group. Deleting a Front Door profile
takes several minutes. A resource group passed in through `resource_group_name` stays, and so
do the app identity and the DNS zone, which are not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the composition:
  `readme_example` renders the example above from the example inputs; `ci_run` uses the longest
  instance a proof produces, checking that the account's name fits, that everything lands in
  the scenarios resource group and that the account and profile carry the tags reap reads;
  others check that the public endpoint is off without app networks and answers only them with
  some, that Front Door's origin is this account over Private Link, that one TTL sets both
  artifactweave's URLs and the edge's cap, and that a custom domain becomes the endpoint
  artifactweave signs for. The custom domain's certificate and records are checked only here:
  a managed certificate can take an hour to issue, which a proof would pay for by the hour.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, a module it calls or `frontdoor_test.go`) runs the `test` lifecycle only: it applies
  an instance as the CI deployer, which also runs artifactweave from the runner, whose address
  is the one network the account answers, runs `test/platform`, destroys the instance, and
  checks that nothing of the run remains. Before artifactweave starts,
  [`frontdoor_test.go`](../../../../../test/platform/frontdoor_test.go) approves the private
  endpoint connection and waits until the runner can write to the account and the edge reaches
  it. The generic checks' redirected downloads, whole and ranged, then go through Front Door,
  and a URL without its SAS is refused. The CDN checks of
  [`cdn_test.go`](../../../../../test/platform/cdn_test.go) follow: `cdnRedirect` alone, the
  `S3` header landing on the account, filenames kept through the edge cache, an expired SAS
  refused, and `jf rt dl` across the edge. `frontdoor_test.go` adds the two only a real Front
  Door shows: a URL the edge has cached stops being served once its SAS and the cache cap have
  passed, and with the account's public endpoint turned off, the account refuses a direct
  request while the edge still serves a new URL over Private Link.
- **Harness outputs** (`harness.tf`): `expires_at`, the tag reap reads from the state.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks against
  an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).
  The profile bills by the hour, so destroy it when done.

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `subscription_id` | `PLATFORM_AZURE_SUBSCRIPTION_ID` | `LAB_AZURE_SUBSCRIPTION` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `awscn-<instance>` | `artifactweave-cdn` |
| `location` | `uksouth` | `uksouth` |
| `resource_group_name` | `PLATFORM_AZURE_RESOURCE_GROUP`, the bootstrap's scenarios group | none: `artifactweave-cdn-rg` |
| `app_principal_id` | the deployer, from its token's `oid` claim | the bootstrap's conformance identity |
| `additional_principals` | none | you, as `operator` |
| `app_ip_ranges` | the runner's public address, from `checkip.amazonaws.com` | your public address, the same way |
| `tags` | `$SCENARIO_LABELS`: `artifactweave-scenario`, `instance`, `expires-at` | none |

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| azurerm | ~> 5.9 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| azurerm | 5.9.0 |
| random | 3.9.1 |

### Modules

| Name | Source | Version |
| ---- | ------ | ------- |
| cdn | ../../modules/azure/cdn | n/a |
| storage | ../../modules/azure/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [azurerm_resource_group.this](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/resource_group) | resource |
| [random_string.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/string) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| additional\_principals | Further principals that get Storage Blob Data Contributor on the account, by a static name, for example an operator who runs artifactweave locally. | ```map(object({ principal_id = string principal_type = optional(string) }))``` | `{}` | no |
| app\_ip\_ranges | Public IPv4 addresses or CIDR ranges (/30 or wider) artifactweave reaches the account from. With these and app\_subnet\_ids both empty, the account's public endpoint is off and artifactweave needs a private endpoint of its own. Azure applies IP rules only to requests from other regions. | `list(string)` | `[]` | no |
| app\_principal\_id | Object ID of the identity artifactweave runs as (its managed identity's principal ID, or a service principal's object ID). It gets Storage Blob Data Contributor on the account. | `string` | n/a | yes |
| app\_subnet\_ids | Subnet IDs, with the Microsoft.Storage service endpoint, that artifactweave reaches the account from: how a host in the account's own region gets through. | `list(string)` | `[]` | no |
| container | Blob container artifactweave writes to (blob.azure.container). | `string` | `"artifactweave"` | no |
| dns\_zone\_id | Resource ID of the Azure DNS zone holding domain\_name. Set it with domain\_name, and only then. | `string` | `null` | no |
| domain\_name | A host of your own for the CDN, such as cdn.example.com, in the Azure DNS zone dns\_zone\_id: a sibling of artifactweave's host, never a subdomain of it. Front Door validates it and issues its certificate asynchronously, which can take an hour. Null serves from the endpoint's own *.azurefd.net host. | `string` | `null` | no |
| expiring\_prefixes | Blob prefixes, from the container's root, whose blobs expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| location | Region of the storage account, for example uksouth. Front Door itself is global. | `string` | n/a | yes |
| name | Start of every name: the Front Door profile and endpoint, the resource group (<name>-rg) when resource\_group\_name is null, and up to 15 letters and digits of the storage account's. | `string` | n/a | yes |
| private\_link\_location | Region where Front Door's private endpoint to the account terminates; it must be one where Front Door offers Private Link origins. Null uses location. | `string` | `null` | no |
| redirect\_ttl\_minutes | How long a signed URL lives, in minutes: artifactweave's download.redirect.ttl, and how long a Front Door edge keeps a copy. A URL is then served at most this long after its SAS expires (decision 0029). | `number` | `5` | no |
| resource\_group\_name | An existing resource group to create everything in. Null creates <name>-rg in location. | `string` | `null` | no |
| store\_prefix | Blob prefix artifactweave writes under (blob.azure.prefix). Front Door serves only the committed blobs under it. | `string` | `""` | no |
| subscription\_id | The subscription to create everything in, passed to the azurerm provider so a CLI login that points elsewhere cannot create anything there. | `string` | n/a | yes |
| tags | Tags for the resource group, the storage account, the Front Door profile and endpoint, and the DNS records. | `map(string)` | `{}` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| account | The storage account (blob.azure.account). |
| artifactweave\_config | The blob and download sections of artifactweave.yaml: one azure store, the account, with Front Door in front of it, and the URL lifetime the edge's cache cap matches. It authenticates through the Azure SDK's default chain, as the app identity, and signs user delegation SAS. |
| container | The container in the account (blob.azure.container). |
| expires\_at | For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| front\_door\_endpoint\_host | The Front Door endpoint's own *.azurefd.net host, which domain\_name's CNAME points at. |
| front\_door\_profile | The Front Door profile's name. |
| origin\_host | The account's blob endpoint, where a downloadRedirect repository's URLs point when a client sends X-JFrog-Download-Redirect-To: S3; it answers only artifactweave's networks. |
| private\_link\_request\_message | The request on Front Door's private endpoint connection to the account, which the approval looks for. |
| resource\_group | The resource group holding the account and the Front Door profile. |
| storage\_account\_id | The storage account's resource ID, where Front Door's private endpoint connection waits for approval. |
| storage\_host | Where a downloadRedirect or cdnRedirect repository's signed URLs point: the CDN host, domain\_name or the endpoint's *.azurefd.net host. |
<!-- END_TF_DOCS -->
