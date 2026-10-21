# Terraform backend

Research baseline: **2026-10-19**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-19, by [#531](https://github.com/weaveplatform/artifactweave/issues/531); [section 9](#9-artifactory-7x-check) records the check.

A `terraformbackend` local repository stores Terraform state for Terraform's `remote` backend.
Research [10](10-scope-and-tiers.md) and [12](12-open-questions.md) initially cut it from version 1
because it needs the Terraform Enterprise (TFE) state API. The maintainer brought it into P8
on 2026-10-19 (epic [#526](https://github.com/weaveplatform/artifactweave/issues/526)).
JFrog documents how to point Terraform at the repository and almost nothing of the wire
protocol, so most of this report comes from Terraform's own client, from HashiCorp's API
documentation, from requests that JFrog's Jira and JFrog's support logs show, and from
running Terraform against a recording server. Decision
[0041](decisions/0041-terraform-backend.md) records the choices.

**Roadmap ownership (2026-10-21).** State-version outputs belong to P28. The unserved
TFE cloud/runs/configuration/variable/project/tag capabilities remain a separate P28
contract and execution-service work item; local execution with remote state does
not provide them. P21 owns browser login, and P28/P41 native OpenTofu qualification.
Decision [0041](decisions/0041-terraform-backend.md#owned-follow-up) preserves the
target's local-only repository-class restriction and separates the old generic
Artifactory backend's migration from this dedicated backend.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited URL, read on 2026-10-19 |
| **[S]** | seen in source code at the cited commit, or observed running that client (section 1.5) |
| **[J]** | recorded in JFrog's public Jira, read on 2026-10-19; it counts as 7.x behaviour under the rules in the [research README](README.md) |
| **[C]** | seen in a community report at the cited URL: a user's capture of what Artifactory sent or logged, not JFrog's statement |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **Terraform** `hashicorp/terraform` **v1.16.6** (`7b8f1669b951`), the latest release on
  2026-10-19: `internal/backend/remote/backend.go`, `backend_state.go`, `backend_context.go`
  and `retry.go`, `internal/command/login.go`, and `internal/cloud/backend.go` for
  comparison; `backend_state.go` at
  v1.2.9, v1.3.0, v1.5.7 and v1.6.0 for the history in section 1.8.
- **go-tfe** `hashicorp/go-tfe` **v1.110.0** (`908c574bd976`), the version Terraform 1.16.6's
  `go.mod` requires. From this release the whole v1 package is one file, `v1.go`, "the final
  version of the go-tfe (v1) package"; the former `tfe.go`, `workspace.go`,
  `state_version.go`, `organization.go` and `errors.go` are in it, and the line numbers below
  are `v1.go`'s. `hashicorp/jsonapi` at `81a76b606f3e`, which both pin, for `request.go`.
- **terraform-svchost** **v0.2.1**, `disco/disco.go` and `disco/host.go` (service discovery).
- **OpenTofu** **v1.13.1**, `internal/backend/remote/backend.go` and `backend_state.go`.
- **HashiCorp's API documentation**, `hashicorp/web-unified-docs` at `f17b63b7b01c`:
  `content/terraform-docs-common/docs/cloud-docs/api-docs/`
  [`index.mdx`](https://github.com/hashicorp/web-unified-docs/blob/f17b63b7b01c6f3ac1fc32dc0a7bc4cc0763aee4/content/terraform-docs-common/docs/cloud-docs/api-docs/index.mdx),
  [`workspaces.mdx`](https://github.com/hashicorp/web-unified-docs/blob/f17b63b7b01c6f3ac1fc32dc0a7bc4cc0763aee4/content/terraform-docs-common/docs/cloud-docs/api-docs/workspaces.mdx)
  and
  [`state-versions.mdx`](https://github.com/hashicorp/web-unified-docs/blob/f17b63b7b01c6f3ac1fc32dc0a7bc4cc0763aee4/content/terraform-docs-common/docs/cloud-docs/api-docs/state-versions.mdx).
- **JFrog's documentation**:
  [terraform-opentofu-and-terraform-backend-repositories](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md)
  (updated 2026-06-08),
  [configuration-json-files](https://docs.jfrog.com/integrations/docs/configuration-json-files.md)
  (2026-07-26), [local-repositories](https://docs.jfrog.com/artifactory/docs/local-repositories.md)
  (2026-09-15),
  [repositories-configurations-in-artifactory-yaml](https://docs.jfrog.com/installation/docs/repositories-configurations-in-artifactory-yaml.md)
  (2026-05-21), [cleanup-policies](https://docs.jfrog.com/administration/docs/cleanup-policies.md)
  (2026-10-01), the API reference pages
  [getrepositoryconfigurationv2](https://docs.jfrog.com/artifactory/reference/getrepositoryconfigurationv2.md)
  and [createrepository](https://docs.jfrog.com/artifactory/reference/createrepository.md)
  (2026-05-28), [artifactory-deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations.md),
  the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues.md) and the
  release notes.
- **JFrog's documentation as the Internet Archive kept it**, for pages that have gone:
  [View Individual Terraform Workspace Information](http://web.archive.org/web/20251012021944/https://jfrog.com/help/r/jfrog-artifactory-documentation/view-individual-terraform-workspace-information)
  (2025-10-12) and
  [Configure Terraform Backend Repository with Artifactory Using Terraform Client](http://web.archive.org/web/20250518224505/https://jfrog.com/help/r/jfrog-artifactory-documentation/configure-terraform-backend-repository-with-artifactory-using-terraform-client?contentId=NA3~gCxhsq_2mIslTh39YA)
  (2025-05-18).
- **JFrog's blog** [Tour Terraform Registries in Artifactory](https://jfrog.com/blog/tour-terraform-registries-in-artifactory)
  (2022-10-26).
- **JFrog's public Jira**, read through `https://jfrog.atlassian.net/rest/api/2/issue/<KEY>`:
  RTFACT-16947, RTFACT-19768, RTFACT-27352, RTFACT-29549, RTFACT-29616, RTFACT-29721,
  RTFACT-29763, RTFACT-29974, RTFACT-30122, RTFACT-30126, RTFACT-30520 and RTFACT-31100.
- **Community reports**: a pasted Artifactory discovery document in
  [aquasecurity/trivy discussion 7982](https://github.com/aquasecurity/trivy/discussions/7982)
  (2024-11-21), and an Artifactory log quoted in
  [HashiCorp Discuss 59096](https://discuss.hashicorp.com/t/terraform-artifactory-backend-storage-broken/59096)
  (2023-10-13).
- **terraform-provider-artifactory** v12.11.14:
  `pkg/artifactory/resource/repository/local/resource_artifactory_local_generic_repository.go`,
  `pkg/artifactory/resource/repository/default_repo_layout_map.go`,
  `pkg/artifactory/resource/configuration/resource_artifactory_package_cleanup_policy.go`,
  `resource_artifactory_archive_policy.go` and `docs/resources/local_terraformbackend_repository.md`.

Artifactory's Terraform backend is closed source and newer than 6.20, so the open-source
edition holds nothing for it.

## 1. The client: Terraform's `remote` backend

### 1.1 Configuration

| Fact | Basis |
|---|---|
| The block is `backend "remote" { hostname, organization, token, workspaces { name \| prefix } }`; `organization` is required, `hostname` defaults to `app.terraform.io` | [S] `backend.go:115-155`, `:236-240` |
| `PrepareConfig` refuses an empty `organization`, a `workspaces` block with neither `name` nor `prefix`, and one with both | [S] `backend.go:158-204` |
| JFrog's form sets `organization` to the repository key and uses a prefix: `backend "remote" { hostname = "johnf.jfrog.io" organization = "tb-local" workspaces { prefix = "jfrog-" } }`, after `terraform login <domain>` | [D] Internet Archive, Configure Terraform Backend Repository ... Using Terraform Client (2025-05-18) |
| Jira reports use both forms: `workspaces { prefix = "my-prefix-" }` and `workspaces { name = "teststatebug" }` | [J] RTFACT-30122, RTFACT-29974 |
| The token comes from the block's `token`, else the CLI credentials for the host (`credentials.tfrc.json`, or `TF_TOKEN_<host>`); with none, Terraform stops with "Required token could not be found" and never calls the server, so the backend never sends an anonymous request | [S] `backend.go:290-328` |
| JFrog's current page gives `terraform login` and the credentials file for the backend, and an access token "to ... set up Artifactory as the remote state and locking provider" | [D] terraform-opentofu-and-terraform-backend-repositories |
| With `prefix`, Terraform's workspace `dev` is the remote workspace `<prefix>dev`, and the `default` workspace is refused; with `name`, only `default` exists and maps to that name | [S] `backend.go:622-667` |

### 1.2 Service discovery

| Fact | Basis |
|---|---|
| The `remote` backend asks the host for the service `tfe.v2.1`, as Terraform 1.0.0 already did; with `TF_FORCE_LOCAL_BACKEND` set it asks for `state.v2` instead | [S] `backend.go:38-44`, `:258-268`; v1.0.0 `backend.go:36-37` |
| The document is `https://<host>/.well-known/terraform.json`, answered 200 with `Content-Type: application/json`, at most 1 MiB | [S] `disco.go:30-39`, `:303-332` |
| A missing `tfe.v2.1` fails discovery; when another `tfe.*` key is present the error is "version not supported", otherwise "service not provided" | [S] `host.go:86-121` |
| A service URL may be relative to the document or absolute, `http` or `https`, with no user information | [S] `host.go:273-295` |
| Version constraints come from a `versions.v1` service; without one Terraform skips the check | [S] `host.go:311-410`, `backend.go:404-406` |
| The `cloud` block (not the `remote` backend) asks for `tfe.v2` | [S] `internal/cloud/backend.go:44` |
| The service URL becomes go-tfe's base: the address is the URL and the base path its path, with a trailing slash added | [S] `backend.go:332-338`; go-tfe `v1.go:21327-21335` |

### 1.3 go-tfe on the wire

| Fact | Basis |
|---|---|
| Every API request carries `Authorization: Bearer <token>`, `Accept: application/vnd.api+json`, `User-Agent: go-tfe` and Terraform's `Terraform-Version` header; POST, PATCH and DELETE add `Content-Type: application/vnd.api+json` | [S] `v1.go:21214-21294`, `backend.go:340-341`; observed |
| Creating the client sends `GET <base>ping` once and reads only headers: `TFP-API-Version`, `X-RateLimit-Limit`, `X-TFE-Version`, `X-TFE-Current-Version` and `TFP-AppName`. It ignores the status, and fails only when the connection fails | [S] `v1.go:21372-21391`, `:21675-21709` |
| `X-RateLimit-Limit` sets a client-side rate limiter; without it the client does not limit itself | [S] `v1.go:21712-21734` |
| A 429 is retried; once the backend is configured, so is any status of 500 or above and a connection error, up to 30 times | [S] `v1.go:21362-21370`, `:21589-21600`; `backend.go:380-381` |
| Responses are decoded as JSON:API; a resource whose `type` differs from the expected one (`workspaces`, `state-versions`, `entitlement-sets`) is a decode error | [S] `hashicorp/jsonapi` `request.go:355-358` |
| A list reads `meta.pagination` (`current-page`, `next-page`, `total-pages`) | [S] `v1.go:21839-21961` |
| Request bodies are JSON:API documents; a struct without a `primary` tag, such as the lock options, is sent with `"type": ""` | [S] `v1.go:21785-21837`; observed |

### 1.4 Statuses and errors

go-tfe turns a status into an error before Terraform sees it ([S] `checkResponseCode`,
`v1.go:21965-22035`; `decodeErrorPayload`, `:22037-22067`):

| Status | What the client reports |
|---|---|
| 200 to 399 | success |
| 400 | the payload's messages; "include parameter" maps to its own error |
| 401 | `unauthorized`, whatever the body |
| 404 | `resource not found`, whatever the body |
| 409 on `.../actions/lock` | `workspace already locked`, whatever the body |
| 409 on `.../actions/unlock` | "unable to unlock workspace locked by run", "... team" or "... user" when a message contains "is locked by Run", "is locked by Team" or "is locked by User"; otherwise `workspace already unlocked` |
| 409 on `.../actions/force-unlock` | `workspace already unlocked` |
| any other | the payload's messages |

The payload's messages are read from JSON:API error objects (`title`, then
`"<title>\n\n<detail>"` when a detail is present), else from `{"errors": ["..."]}`, else
the status line, such as `403 Forbidden` ([S] `v1.go:22037-22067`). An unlock that fails
with a message containing "latest state version is still pending" becomes
`ErrWorkspaceLockedStateVersionStillPending` ([S] `v1.go:25502-25509`), and Terraform retries
that one unlock every three seconds for as long as it is answered ([S] `backend_state.go:226-237`,
`retry.go:36-98`, whose context has no deadline).

### 1.5 The calls each command makes

Observed on 2026-10-19 with Terraform 1.16.6 (`darwin_arm64`) and the CLI configuration's
`host` block pointing `state.v2`, `tfe.v2`, `tfe.v2.1` and `tfe.v2.2` at a recording server
under `/artifactory/api/terraform/remote/v2`, as the 7.x discovery document does (section
3.2). The server answered as section 1.6 shows: entitlements with `operations: false`,
workspaces with `execution-mode: "local"`, and a state-less state-version create with an
upload URL. Configurations used `workspaces { prefix = "app-" }` and `workspaces { name =
"solo" }`, one `terraform_data` resource and one output ([S] for every row).

Every command that configures the backend first sends `GET ping` and `GET
organizations/<org>/entitlement-set`; the table leaves those two out. `<id>` is the
workspace ID the server returned, `<sv>` a state-version ID.

| Command | Further calls, in order |
|---|---|
| `init`, prefix | `GET organizations/<org>/workspaces?search[name]=<prefix>`. With no match Terraform stops: "No existing workspaces", exit 1 |
| `init`, name | `GET organizations/<org>/workspaces/<name>` → 404; `POST organizations/<org>/workspaces` → 201; `GET workspaces/<id>/current-state-version` → 404 |
| `workspace new <ws>` | the list; `GET organizations/<org>/workspaces/<prefix><ws>` → 404; `POST organizations/<org>/workspaces` → 201. No state is written |
| `workspace list`, `workspace select` | the list only |
| `workspace show` | none |
| `workspace delete <ws>` | the list; `GET .../workspaces/<prefix><ws>`; `POST workspaces/<id>/actions/lock`; `GET workspaces/<id>/current-state-version` (Terraform refuses a workspace that still manages resources unless `-force`); `POST workspaces/<id>/actions/unlock`; `DELETE organizations/<org>/workspaces/<prefix><ws>` → 204 |
| `plan` | `GET .../workspaces/<name>` twice; `POST .../actions/lock`; `GET .../current-state-version`, and the download when there is one; `POST .../actions/unlock` |
| `apply`, `destroy` | as `plan`, then, when the state changed: `POST workspaces/<id>/state-versions` → 201; `PUT <hosted-state-upload-url>` → 200; `GET state-versions/<sv>`; then the unlock |
| `state pull` | `GET .../workspaces/<name>`; `GET .../current-state-version`; `GET <hosted-state-download-url>`. No lock |
| `state push <file>` | `GET .../workspaces/<name>` twice; lock; current state and its download; when the pushed state differs, the create, upload and re-read as for `apply`; unlock. An identical state writes nothing; another lineage is refused by Terraform itself without `-force` ("cannot import state with lineage ... over unrelated state"), and sent with `force: true` with it |
| `plan` while locked | `GET .../workspaces/<name>` twice; `POST .../actions/lock` → 409. Nothing else |
| `force-unlock -force <org>/<ws>` | `GET .../workspaces/<prefix><ws>`; `POST workspaces/<id>/actions/force-unlock` |

### 1.6 Bodies and the fields read back

| Call | Request body | What Terraform reads from the answer |
|---|---|---|
| `GET ping` | none | headers only (section 1.3) |
| `GET organizations/<org>/entitlement-set` | none | `data.type` `entitlement-sets`; `attributes.operations`. `false` makes every operation local. A 404 stops `init` with "organization ... not found ... ensure that the organization and hostname are correct and that your API token ... is valid" |
| `GET organizations/<org>/workspaces` | query `search[name]=<prefix or name>`, then `page[number]` for later pages | each `data[].attributes.name`; Terraform keeps the names that start with the prefix and strips it; `meta.pagination` |
| `GET organizations/<org>/workspaces/<name>` | none | `id`, used in every later `workspaces/<id>/...` path; `attributes.terraform-version` and `attributes.execution-mode` (section 1.7); a 404 makes `StateMgr` create the workspace |
| `POST organizations/<org>/workspaces` | `{"data":{"type":"workspaces","attributes":{"name":"app-dev","terraform-version":"1.16.6"}}}`; `terraform-version` is left out by a pre-release build other than a release candidate | the workspace, as above |
| `DELETE organizations/<org>/workspaces/<name>` | none | status only; 404 counts as deleted |
| `POST workspaces/<id>/actions/lock` | `{"data":{"type":"","attributes":{"reason":"Locked by Terraform"}}}` | status only (409 in section 1.4) |
| `POST workspaces/<id>/actions/unlock`, `.../force-unlock` | none | status and, for unlock, the 409 messages |
| `GET workspaces/<id>/current-state-version` | none | `attributes.hosted-state-download-url`; a 404 means no state |
| `GET <hosted-state-download-url>` | none; `Accept: application/json` and the Bearer token, the URL resolved against the base when relative | the raw state; an empty body means no state |
| `POST workspaces/<id>/state-versions`, Terraform 1.6 and later | `{"data":{"type":"state-versions","attributes":{"serial":1,"md5":"<hex of the raw state>","lineage":"<uuid>","force":false,"json-state-outputs":"<base64>"}}}` | `id` and `attributes.hosted-state-upload-url` |
| the same, Terraform 1.5 and earlier, and the fallback (section 1.8) | as above with `"state":"<base64 of the raw state>"`; 1.2 and earlier send no `json-state-outputs` | status only |
| `PUT <hosted-state-upload-url>` | the raw state; `Content-Type: application/octet-stream`, `Accept: application/json, */*`, no `Authorization` | any status from 200 to 399 is success |
| `GET state-versions/<sv>` | none | the answer is discarded once it decodes |

| Fact | Basis |
|---|---|
| The serial sent is the one Terraform writes into the state: a pushed file with serial 2 is sent as 3, and `-force` sends the pushed file's serial plus one even when it is lower than the current one | [S] observed |
| `md5` is the hex MD5 of the raw state bytes, which Terraform also computes for the state it reads | [S] `backend_state.go:75-81`, `:129-137` |
| The upload is a "foreign" PUT: go-tfe parses the URL as it is, so it must be absolute, and sends the client's default headers without the token | [S] `v1.go:21162-21197`, `:18593-18608` |
| `Upload` falls back to the inline form when the state-less create fails with a message containing "param is missing or the value is empty: state", the answer of Terraform Enterprise v202306-1 and earlier | [S] `v1.go:18580-18591`; `backend_state.go:146-153`; observed with a 422 carrying that detail |
| A failed upload leaves the workspace locked on purpose, and Terraform writes `errored.tfstate` and tells the user to `terraform state push` it | [S] `backend_state.go:154-157`, `:207-212`; [J] RTFACT-30122 (the client's output) |
| go-tfe refuses, before sending, an organisation or workspace name or ID that contains `/` or white space | [S] `v1.go:22697-22715`, `:25879-25885` |

### 1.7 Locks and versions as the client sees them

| Fact | Basis |
|---|---|
| The lock request names no lock ID and no operation; the server learns only who locked and the reason, "Locked by Terraform" | [S] `backend_state.go:180-201` |
| A refused lock is reported as `workspace already locked (lock ID: "<org>/<workspace name>")` followed by **the requesting process's own** lock information (its ID, operation, user and time), not the holder's | [S] `backend_state.go:183-195`; observed: "Error acquiring the state lock ... workspace already locked (lock ID: "tf-state/app-dev")" |
| `force-unlock` takes that `<org>/<workspace name>` ID, checks it against the configuration, and sends `actions/force-unlock` | [S] `backend_state.go:247-265`; observed |
| A normal unlock is sent only by the process that took the lock | [S] `backend_state.go:216-245` |
| Unless operations are local, every plan and apply would also read `workspaces/<id>/vars`; `execution-mode: "local"` skips it | [S] `backend_context.go:100-135` |
| `StateMgr` refuses a workspace whose `terraform-version` is neither the client's own nor `latest`, unless the command has relaxed the check; plans and applies relax it | [S] `backend.go:696-703`, `:777` |
| `VerifyWorkspaceTerraformVersion`, which `state push` and other state commands run, returns early for `latest` or `execution-mode: "local"`; otherwise it treats 0.14 to 1.2.x as one compatible range and, from 1.3, requires the same minor version | [S] `backend.go:935-1043` |
| Observed: with a workspace that answers only `id`, `type` and `name`, `plan` and `apply` work and `state push` fails with "Incompatible Terraform version"; with `terraform-version: "1.2.9"` and no `execution-mode`, `state push` fails with "Terraform version mismatch"; with `execution-mode: "local"`, or `terraform-version: "latest"`, every command passes | [S] observed |

### 1.8 History of the client

| Fact | Basis |
|---|---|
| Terraform 1.2.9 sends the state inline with no `json-state-outputs`; 1.3.0 adds `json-state-outputs`; 1.5.7 still sends inline | [S] `backend_state.go` at v1.2.9 (`:72`), v1.3.0 (`:84-86`), v1.5.7 (`:87-99`) |
| Terraform 1.6.0 switched to `StateVersions.Upload` with the inline fallback | [S] `backend_state.go` at v1.6.0 (`:110-136`) |
| OpenTofu 1.13.1 asks for the same services and uploads the same way | [S] OpenTofu `backend.go:43-44`, `backend_state.go:110-136` |
| JFrog supports Terraform CLI 1.0.0 or above for the backend; OpenTofu "TBD" | [D] terraform-opentofu-and-terraform-backend-repositories, Supported Clients |

### 1.9 `terraform login` without `login.v1`

The permanent native exit checks in `test/acceptance/features/terraform_backend.feature`
exercise this fallback with Terraform 1.16.4 and the discovery services served by
artifactweave. They feed consent and token separately, matching the interactive prompts:
[`UIInput.Input`](https://github.com/hashicorp/terraform/blob/v1.16.4/internal/command/ui_input.go)
constructs a fresh buffered reader per prompt, so piping both lines together can discard
the second line. The fixture uses the isolated HOME's `.terraformrc` and
`.terraform.d/credentials.tfrc.json` rather than `TF_CLI_CONFIG_FILE`:
[`LoadConfig`](https://github.com/hashicorp/terraform/blob/v1.16.4/internal/command/cliconfig/cliconfig.go)
deliberately ignores the configuration directory when that override is set. Login checks
`account/details`, persists the checked token, and a subsequent native init uses it.

| Fact | Basis |
|---|---|
| `terraform login <host>` uses `login.v1` when the discovery document has it. Without it, it falls back to `tfe.v2`; without that either, it stops with "Host does not support Terraform tokens API" | [S] `login.go:119-165` |
| When a `tfe.*` key other than `tfe.v2` is present and `tfe.v2` is not, the fallback stops with "allows creating Terraform authorization tokens, but requires a newer version of Terraform CLI" | [S] `login.go:152-157`; `host.go:97-109` |
| The fallback asks for consent, then opens `https://<host>/app/settings/tokens?source=terraform-login` (the `tfe.v2` URL's host name, always `https`, no port) in a browser, or prints it, and prompts for a token to paste | [S] `login.go:586-647` |
| It checks the pasted token with go-tfe against the `tfe.v2` URL: `GET <tfe.v2>ping`, then `GET <tfe.v2>account/details` | [S] `login.go:649-660`; go-tfe `v1.go:22665-22678` |
| A 401 stops with "Token is invalid: unauthorized"; any other failure with "Failed to retrieve user account details: <error>", a 404 giving "resource not found"; on success it prints "Retrieved token for user <username>" from the `users` document's `username`, stores the token for the host, and prints "Logged in to Terraform Enterprise (<host>)" | [S] `login.go:660-671`, `:216`, `:286-288`, `:305-314`; go-tfe `v1.go:22609-22617` |
| `login` refuses a host whose credentials are already in a CLI configuration `credentials` block | [S] `login.go:167-173` |
| 7.x's discovery document has `login.v1`, so the fallback never runs against 7.x | [C] trivy discussion 7982; [I] |

## 2. What HashiCorp documents for the TFE API

| Fact | Basis |
|---|---|
| Endpoints use JSON:API documents and error objects; bodies are `application/vnd.api+json` | [D] `index.mdx`, JSON API formatting |
| A missing or invalid token answers 401; "forbidden requests with a valid token result in a 404", and a resource the user cannot see answers 404 like a missing one | [D] `index.mdx`, Authentication and Response codes |
| Lists page with `page[number]` and `page[size]`, 20 per page by default and at most 100 | [D] `index.mdx`, Pagination |
| Lock answers 200 with the workspace, 404, or 409 "Workspace already locked"; unlock 200, 404, or 409 "already unlocked, or locked by a different user"; force-unlock 200, 404, or 409 "already unlocked" | [D] `workspaces.mdx`, Lock, Unlock and Force Unlock a workspace |
| Workspace names "can only include letters, numbers, `-`, and `_`"; `DELETE organizations/:organization_name/workspaces/:name` is the force delete, 204 | [D] `workspaces.mdx`, Create a Workspace, Force Delete a workspace |
| Creating a state version "sets it as the current state version"; "The workspace must be locked by the user creating a state version"; it answers 201, 404, 409, 412 or 422 | [D] `state-versions.mdx`, Create a State Version |
| `serial` and `md5` are required; `serial` "Must match the serial value extracted from the raw state file"; `state` is optional, and without it "you must use the upload method"; "The workspace may not be unlocked normally until the state version is uploaded" | [D] `state-versions.mdx`, Request Body |
| The upload URL takes the state once ("Once you upload state content, this URL is hidden on the resource"); a pending state version blocks unlocking until it is finalized | [D] `state-versions.mdx`, Upload State and JSON State, State Version Status |
| Listing is `GET /state-versions?filter[workspace][name]=&filter[organization][name]=`; the current one is `GET /workspaces/:workspace_id/current-state-version`, 404 when there is none | [D] `state-versions.mdx`; [S] `v1.go:18535-18552`, `:18651-18672` |

## 3. What Artifactory serves

### 3.1 The repository type

| Fact | Basis |
|---|---|
| The Terraform Backend repository is "a dedicated Remote State Storage Provider", from 7.38.4 | [D] terraform-opentofu-and-terraform-backend-repositories |
| "There is a dedicated Repository type for Terraform BE. Supports only Local repositories. Remote and Virtual repositories are not supported"; "Federated Repositories are not supported for Terraform Backend Repositories" | [D] same page, Create ... and Terraform Limitations |
| Its benefits: multiple workspaces, "Built-in Secure State Encryption storage", "Comprehensive State snapshot history", "State content viewer"; locking, "encoding of the data, smart comparisons" | [D] same page; [D] artifactory-deprecations |
| JFrog's backend is "unaffected by" HashiCorp's 2022 deprecation of the legacy `artifactory` backend, which stored state in a generic repository | [D] artifactory-deprecations, "Terraform Backend Repository deprecation by Hashicorp" |
| Locking was requested for the legacy backend in 2019 and delivered with the new type | [J] RTFACT-19768, fixed in 7.38.0 and 7.38.6 |

### 3.2 Discovery and the observed endpoints

| Fact | Basis |
|---|---|
| Artifactory's `/.well-known/terraform.json` gives `state.v2`, `tfe.v2`, `tfe.v2.1` and `tfe.v2.2` all as `https://<host>/artifactory/api/terraform/remote/v2`, beside `modules.v1` and `login.v1` (the capture predates `providers.v1`) | [C] trivy discussion 7982 (2024-11-21); [research 17](17-undocumented-shapes.md#terraform-discovery-and-the-remote-backend) |
| A 7.41.6 request log shows `POST /api/terraform/remote/v2/organizations/terraform-backend/workspaces` answered 201, with `go-tfe` as the agent | [J] RTFACT-27352 |
| A request log from 7.68.11 or 7.71.4 shows `POST /api/terraform/remote/v2/workspaces/test-terraform-local__my-prefix-test/state-versions`: the workspace ID is `<repository key>__<remote workspace name>` | [J] RTFACT-30122 |
| Artifactory classes `TerraformAddonImpl` and `TerraformLocalStateRepoHandler` read and write the state, and the state request is bound to a model `StatePayload` with `data.attributes.state` | [J] RTFACT-29974, RTFACT-29763; [C] HashiCorp Discuss 59096 |
| Which other TFE paths 7.x serves (`ping`, `GET organizations/<org>`, `workspaces/<id>`, `state-versions` lists) and with which bodies | [I] |

### 3.3 Storage

| Fact | Basis |
|---|---|
| "Artifactory automatically generates a state.json file when a workspace.json is deployed. The latest file is declared as the state.latest.json and all previous states are renamed with a timestamp indicating the time they were created, for example, state.1640018380463.json" | [D] Internet Archive, View Individual Terraform Workspace Information (2025-10-12) |
| Paths in JFrog's logs: `test-terraform-be/workspaces/abcd/state.1684124654453.json`, `terraform-backend/workspaces/sample-tf/state.latest.json`, `terraform-state/workspaces/acr-cicd/state.latest.json` | [J] RTFACT-29974, RTFACT-29763; [C] HashiCorp Discuss 59096 |
| JFrog's blog shows "the state.latest.json of our current active state and the state.timestamp.json of our previous state within the tf-backend repository and the jfrog-k8s-webinar workspace", with "who created it, when it was created" per state | [D] blog, Tour Terraform Registries in Artifactory |
| A workspace keeps a `workspace.json` that records a Terraform version: a reporter's workaround was to create the workspace with 1.2.9 so that "the workspace.json remains untouched an still references verision 1.2.9" | [J] RTFACT-29616 (closed without a fix version or a JFrog comment) |
| The workspace folder is the remote workspace's full name, prefix included | [I] from the request paths of RTFACT-30122 and the folder names above |
| What `workspace.json` holds, and which properties a state file carries | [I] |
| The UI shows a workspace's metadata in a "Terraform Info" tab and in the Packages view | [D] Internet Archive, View Individual Terraform Workspace Information |
| A `.tfstate` MIME type was requested for the legacy backend; the request is still open | [J] RTFACT-16947 |

### 3.4 Locking

| Fact | Basis |
|---|---|
| "A property called locked.by is added with the value of the user performing the configuration change to the workspace"; "any other user will be denied updating the workspace during this time"; "the terraform client will return an error with the message that it is currently locked by the user"; "Lock will be released once the changes to the workspace is completed" | [D] blog, Tour Terraform Registries in Artifactory |
| "the locked.by property together with the name of the user performing the change is displayed in the Properties tab of the Workspace" | [D] Internet Archive, View Individual Terraform Workspace Information |
| Whether `locked.by` sits on the folder or on `workspace.json`, what the 409 body says, and whether a user may delete the property to unlock | [I] |

### 3.5 Encryption

| Fact | Basis |
|---|---|
| "The state files are encrypted using the Master Key"; "If the master.key is lost the state files that rely on it for encryption are not recoverable" | [D] terraform-opentofu-and-terraform-backend-repositories |
| `encryptStates` is a local repository field, `true` by default; it is a boolean in the create, v1 and v2 read schemas | [D] configuration-json-files; [D] createrepository, getrepositoryconfigurationv2 |
| "Replication of Terraform Backend Repositories is fully supported from Artifactory version 7.77.x and later. To perform replication ... between two different Artifactory platforms, you must disable the `encryptStates` flag", in the Local Repository Configuration JSON | [D] terraform-opentofu-and-terraform-backend-repositories |
| A `state.latest.json` copied to another instance cannot be read there: "javax.crypto.BadPaddingException: Given final block not properly padded. Such issues can arise if a bad key is used during decryption", logged reading "the current state version"; the workaround is `terraform state pull` from the old instance and `terraform state push` to the new one | [J] RTFACT-29763 (Done, no fix version) |
| A repository converted to federated could not be read on the other member ("Bad Key encryption", 400 on `terraform init`); the issue was closed | [J] RTFACT-29721 |
| Which algorithm and mode 7.x uses; a padding error suggests a block cipher with PKCS#5 padding | [I] |
| Whether a download of an encrypted state through `/artifactory/<repo>/...` answers the stored bytes or the plain state, which checksums the item shows, and what turning `encryptStates` off does to states already stored | [I] |

### 3.6 Size

| Fact | Basis |
|---|---|
| On 7.63.14 a state over 20 MB failed: "Failed to read state file from test-terraform-be/workspaces/abcd/state.1684124654453.json: String length (20054016) exceeds the maximum length (20000000) (through reference chain: ...StatePayload["data"]->...["attributes"]->...["state"])", seen by Terraform as "Error retrieving state: 400" | [J] RTFACT-29974 |
| JFrog fixed it: "Artifactory Product Fix Version: 7.68.11", posted by JFrog's Yarden Gitta | [J] RTFACT-29974 |
| 20,000,000 characters is the default maximum string length of the JSON parser named in the trace (Jackson's `StreamReadConstraints`), applied to the base64 `state`; it was not a designed limit | [I] |
| Whether 7.x now has any limit on a state's size | [I] |

### 3.7 Client versions and tokens

| Fact | Basis |
|---|---|
| Terraform 1.6.0 and 1.6.1 failed against Artifactory SaaS: "Failed to create new state version at terraform-state/workspaces/acr-cicd/state.latest.json: Cannot invoke "String.getBytes(java.nio.charset.Charset)" because "content" is null", and a go-tfe panic in `Upload`; 1.5.7 worked | [C] HashiCorp Discuss 59096 |
| With Terraform 1.6.3, the state-less create answered 400 and the apply failed with "error uploading state: 400"; 1.5.7 worked. The issue is Done with no fix version | [J] RTFACT-30122 |
| On 7.49.10, clients above 1.2.9 failed to push state unless the workspace had been created with 1.2.9. The symptoms match the client's version check (section 1.7), which treats 0.14 to 1.2.x as one range and is off for `execution-mode: "local"` | [J] RTFACT-29616 (closed); [I] the explanation |
| Which of the upload URL and the fallback message 7.x answers today | [I] |
| On 7.41.6 the backend treated requests as anonymous "after a few seconds of activity", so `terraform workspace new` failed with "403 Forbidden"; the expected behaviour is that requests keep the user "until the access token expires" | [J] RTFACT-27352 |
| "Access tokens timeout with a Terraform backend causing a permission error", known issue RTDEV-27518 of 7.41.6, fixed in 7.46.3 | [D] known issues, Artifactory 7.41.6 |

### 3.8 Repository fields and the clients that create them

| Fact | Basis |
|---|---|
| `terraformbackend` is in the local `packageType` list and in no other class's | [D] configuration-json-files; [J] RTFACT-30520 (added to the documentation) |
| The YAML descriptor spells it `terraformbe` | [D] repositories-configurations-in-artifactory-yaml |
| A REST create of a Maven local repository on 7.104.10 read back with `"terraformType": "MODULE"` and `"encryptStates": true`: the v1 read sends `encryptStates` whatever the type | [J] RTFACT-31100 |
| The v2 read returns "only those fields that are relevant to the package type"; whether it keeps `encryptStates` for `terraformbackend` | [D] getrepositoryconfigurationv2; [I] which fields |
| `artifactory_local_terraformbackend_repository` is a generic local resource: `key`, `description`, `notes` and the common local arguments. It sends no `encryptStates`, so a repository it creates gets the default | [S] provider 12.11.14 `resource_artifactory_local_generic_repository.go:30-50`, `:136-138`; `docs/resources/local_terraformbackend_repository.md` |
| The provider's default layout for it is `simple-default`, and it marks it local only | [S] `default_repo_layout_map.go:374-381` |
| "Disable Artifact Resolution in Repository" (`blackedOut`) and CDN download are not supported for Terraform BE; Xray indexing is | [D] local-repositories |
| The JFrog CLI has no backend command: jfrog-client-go's repository parameters have Terraform module and provider repositories and no `terraformbackend` | [S] jfrog-client-go `3b4b900d8174`, `artifactory/services/localrepository.go:138`, `:390-395` |

### 3.9 Cleanup, retention and federation

| Fact | Basis |
|---|---|
| "Terraform BE packages are now supported in Cleanup and Archive" (7.104.5; SaaS 7.103.0) | [D] [7.104.5 notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed); [SaaS 7.103.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71030-saas) |
| The cleanup table lists Terraform BE from 7.103.0 with the lead artifact `.json` and all three conditions; for Keep Last N Versions "The latest version is always excluded" | [D] cleanup-policies, Cleanup Supported Packages |
| The provider's `artifactory_package_cleanup_policy` and `artifactory_archive_policy` accept `terraformbackend` | [S] `resource_artifactory_package_cleanup_policy.go:272`, `resource_artifactory_archive_policy.go:269` |
| What a Terraform BE package and its versions are for cleanup | [I] |
| Keeping only the last N state versions automatically is an open request | [J] RTFACT-30126 (open) |
| A federated backend repository through the UI and the provider is an open request; JFrog's workaround was converting a local one in the UI | [J] RTFACT-29549 (open) |

### 3.10 Release-note history

From 7.84 (self-managed) and 7.80 (SaaS) the notes mention the backend once: Cleanup and
Archive support in 7.104.5 and SaaS 7.103.0 ([D]). Every other Terraform note in 7.84 to
7.161.32 and 7.80 to 7.174.0 concerns modules, providers and remote repositories. The
known issues add RTDEV-27518 (section 3.7). Earlier changes show only through Jira: 7.38.0
and 7.38.6 (RTFACT-19768), 7.46.3 (RTDEV-27518) and 7.68.11 (RTFACT-29974).

## 4. artifactweave at the baseline

- **[S]** `pkg/repo/packagetypes.go:25-30` refuses `terraformbackend` for remote, virtual and
  federated repositories; local repositories accept it.
- **[S]** `pkg/repo/defaults.go:94-96` sets `encryptStates` to `true` on a create of a
  `terraformbackend` repository that leaves it out, and `pkg/repo/views.go:137-140` keeps it
  (with `terraformType` and the Terraform URLs) in the v2 read and the configurations list
  only for `terraform` and `terraformbackend`.
- **[S]** `internal/descriptor/parse.go:359` reads the YAML `type: terraformbe` as
  `terraformbackend`.
- **[S]** `internal/server/stubs.go:23-24` answers unclaimed paths under
  `/artifactory/api/terraform/` in the registry envelope, `{"errors":["..."]}`, which go-tfe
  reads as messages too (section 1.4).
- **[S]** `internal/crypt` seals values with AES-256-GCM under keys derived from the master
  key, with a key version and a purpose and row as additional data (decision
  [0017](decisions/0017-sealed-columns-and-master-key.md)).
- **[S]** `internal/service/multipart` signs part URLs that the JFrog CLI PUTs without
  credentials, the pattern an upload URL needs.
- **[S]** `internal/server/server.go:215-218` limits JSON management bodies to 64 MiB and
  answers 413 above it.
- **[S]** No handler serves `/artifactory/api/terraform/remote/v2/`, and the discovery
  document is #553's.

## 5. Corrections to research 03, 04, 10, 12 and 17

- Research 04's "State backend" said the package type was cut from version 1 and listed the
  `tfe.v2` paths as unverified. It now points here: the client's calls are known from source
  and observation (section 1), and the paths 7.x serves are partly observed (section 3.2).
- Research 17's discovery entry now gives `state.v2` and the `tfe.v2*` keys as artifactweave's
  (decision 0041), and the backend sub-paths as captured from the client.
- Research 10's and 12's cut, and research 03's note that the type "would need the
  Terraform Enterprise state API", are replaced by decision 0041. Research 03's `encryptStates`
  row adds what the flag seals and the replication rule.
- Research 08's statement about how the provider's `terraformbackend` create is answered
  belongs to #530 and is not changed here.

## 6. Inferred and undocumented

These go to the live-capture list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)):

**Paths and bodies**
1. Which TFE paths 7.x serves beyond the two observed (`POST organizations/<org>/workspaces`,
   `POST workspaces/<id>/state-versions`): `ping` and its headers, `organizations/<org>`,
   `entitlement-set` and its attributes, the workspace list and its paging, `workspaces/<id>`,
   `current-state-version`, the download URL, `state-versions/<id>` and the list, and
   `account/details`.
2. The workspace document 7.x answers: `execution-mode`, `operations`, `terraform-version`,
   `locked`, timestamps and permissions.
3. Whether 7.x answers a state-less create with `hosted-state-upload-url` or with the
   "param is missing or the value is empty: state" fallback, and what its upload URL looks
   like and accepts.
4. The error body and `Content-Type` of 7.x's 400, 403, 404, 409 and 422 answers on these
   paths, and whether a missing workspace or one the caller cannot read answers 404 or 403.
5. How 7.x splits a workspace ID `<repo>__<ws>` when the repository key or the workspace
   name contains `__`, and which workspace names it accepts.

**Storage and locking**
6. What `workspace.json` holds, which properties the workspace folder and the state files
   carry, and whether `locked.by` is on the folder or on `workspace.json`.
7. Whether the timestamp in `state.<epoch-ms>.json` is the state's creation time or the time
   it was superseded.
8. Whether 7.x refuses a state write from a user other than the lock holder, and a write
   while the workspace is unlocked (`-lock=false`).
9. Whether a user may unlock by deleting `locked.by`, and which permission lock, unlock and
   force-unlock need.
10. Which permissions each operation needs on 7.x: Read for `plan` (which locks), Deploy for
    state writes, Delete for workspace delete.
11. What a deploy, copy or move into a `terraformbackend` repository through the item routes
    answers, and what deleting `state.latest.json` through REST does to the workspace.

**Encryption and size**
12. The algorithm and mode `encryptStates` uses; what a download of an encrypted state
    through `/artifactory/<repo>/...` answers; the checksums the item shows.
13. What turning `encryptStates` off or on does to states already stored, and whether a
    REST update may change it.
14. Whether 7.x limits a state's size after the 7.68.11 fix, and the status above a limit.

**Repository fields and cleanup**
15. Whether the v2 read keeps `encryptStates` for `terraformbackend`, and what a create
    without `repoLayoutRef` gets.
16. What a Terraform BE "package" and "version" are for cleanup and archive, and whether
    `state.latest.json` can ever be a cleanup candidate.

**Clients**
17. Whether the `cloud` block (`tfe.v2`) works against 7.x.

## 7. Versions at the research baseline

| Component | Version |
|---|---|
| Terraform | v1.16.6 (`7b8f1669b951`) |
| go-tfe | v1.110.0 (`908c574bd976`) |
| hashicorp/jsonapi | `81a76b606f3e` |
| terraform-svchost | v0.2.1 |
| OpenTofu | v1.13.1 |
| HashiCorp API docs | `hashicorp/web-unified-docs` `f17b63b7b01c` |
| terraform-provider-artifactory | v12.11.14 |

## 8. Recording the client

Section 1.5 came from Terraform 1.16.6 run against a small recording server in the
researcher's scratch space, not in the repository. Its answers were the minimum section 1.6
names; it gave workspace IDs in 7.x's `<repo>__<ws>` form and an absolute upload URL with a
query token. Variants checked: the inline fallback (a 422 with the fallback message), a
workspace with only `id`, `type` and `name`, `terraform-version: "1.2.9"` with and without
`execution-mode: "local"`, and `terraform-version: "latest"`. #558's server test replays the
same sequences.

## 9. Artifactory 7.x check

Checked against Artifactory 7.161.32 on 2026-10-19 ([#531](https://github.com/weaveplatform/artifactweave/issues/531)).

**Sources.** JFrog's current documentation, read on 2026-10-19 (the pages named above), the
Artifactory release notes, self-managed 7.84.3 to 7.161.32
([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases))
and SaaS 7.80.0 to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)),
the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) and the
[deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations); the Jira
issues above; and the Internet Archive's copies of two JFrog pages that have gone. No JFrog
KB article on the backend was found. The latest releases on 2026-10-19 are 7.161.32
(self-managed, 6 October) and 7.174.0 (SaaS, 15 October); neither changes the backend.

**Confirmed for 7.x:**
- local repositories only, with `encryptStates` true by default and the master key's
  encryption;
- `backend "remote"` with `organization` set to the repository key, through the
  `/api/terraform/remote/v2` service the discovery document names;
- workspaces under `workspaces/<ws>/` with `state.latest.json` and timestamped earlier
  states, and the `locked.by` property;
- replication needs `encryptStates` false (7.77);
- cleanup and archive cover Terraform BE packages (7.104.5, SaaS 7.103.0).

**Fixed in 7.x, so not reproduced:** the 20,000,000-character state limit (7.68.11), and
requests falling back to anonymous before the token expires (7.46.3).

**Unconfirmed for 7.x:** every **[I]** above and the list in section 6, which stay on
[#216](https://github.com/weaveplatform/artifactweave/issues/216).


## 10. API implementation check (#558)

The implementation follows decision 0041's corrected route and upload forms.
HashiCorp's current [state-version API](https://developer.hashicorp.com/terraform/cloud-docs/api-docs/state-versions),
[workspace API](https://developer.hashicorp.com/terraform/cloud-docs/api-docs/workspaces)
and [service discovery](https://developer.hashicorp.com/terraform/internals/remote-service-discovery)
were rechecked on 2026-10-21. The two state-upload forms, workspace relationships,
local execution mode, JSON:API types and filtered state-version listing are retained.
The Artifactory-specific permission and lock choices remain the decision's inferences;
this implementation does not turn those into newly observed 7.x evidence.

`TestRemoteBackendSequence` replays the captured calls against the API and storage;
`terraform_backend_api.feature` exercises discovery, account details, locking and an
inline state round trip against the real server binary. #559 owns the full native
Terraform exit suite.
