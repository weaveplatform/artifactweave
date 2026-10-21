# 0041: Terraform backend

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-19 ([research 32 section 9](../32-terraform-backend.md#9-artifactory-7x-check), [#531](https://github.com/weaveplatform/artifactweave/issues/531)).

## Context

A `terraformbackend` local repository stores Terraform state for Terraform's `remote`
backend, which speaks the Terraform Enterprise (TFE) API through `go-tfe`. Artifactory 7.x
serves that API under `/api/terraform/remote/v2/`, keeps each workspace's states as items
under `workspaces/<ws>/`, marks a lock with the `locked.by` property, and encrypts states
under the master key unless `encryptStates` is off. JFrog documents the layout, the lock
property and the encryption, and nothing of the API beyond the service the discovery
document names. [Research 32](../32-terraform-backend.md) records what Terraform 1.16.6 and
go-tfe v1.110.0 send and read, what HashiCorp documents for the TFE API, what JFrog's
documentation, blog and Jira show of 7.x, and what no source confirms.

artifactweave already accepts and stores `terraformbackend` repositories, local only, with
`encryptStates` defaulting to `true` (research 32 section 4). Nothing serves their API.
Decision [0017](0017-sealed-columns-and-master-key.md) gives the master key and the sealing
format, [0018](0018-package-index.md) the package index, and
[0032](0032-package-format-handlers.md) the format hooks this decision uses.

## Decision

**Scope.** Local `terraformbackend` repositories serve Terraform's `remote` backend, and
OpenTofu's, which asks for the same services (research 32 section 1.8), for Terraform 1.0
and later as JFrog supports. Not served: the `cloud` block's needs (runs, configuration
versions, variables, projects, tags), remote operations, state-version outputs, and
`login.v1`, which is #262's (until then, see "`terraform login` until #262" below). Remote,
virtual and federated `terraformbackend` repositories stay refused at create, as 7.x
documents.

**Discovery.** The discovery document of decision 0040
([#530](https://github.com/weaveplatform/artifactweave/issues/530); #553's handler) adds
`state.v2`, `tfe.v2`, `tfe.v2.1` and `tfe.v2.2`, each as
`<base>/artifactory/api/terraform/remote/v2`, the four keys and the one value 7.x gives
(research 32 section 3.2). They take decision 0040's form: `<base>` comes from
the base URL resolver, so the values are absolute; the document is a host-root route served
before authentication; and the services may be plain `http` although Terraform fetches the
document itself only over HTTPS, so plain-HTTP acceptance copies them into a
`TF_CLI_CONFIG_FILE` `host` block, as decision 0040 describes. The value has no trailing
slash, as 7.x's; go-tfe adds one (research 32 section 1.2), so either form works. Terraform's
`remote` backend reads `tfe.v2.1`, and `state.v2` under `TF_FORCE_LOCAL_BACKEND`; `terraform
login` reads `tfe.v2` while `login.v1` is absent (research 32 section 1.9); `tfe.v2.2` is
kept for parity. Below, `<tfe>` is `/artifactory/api/terraform/remote/v2/`.

**Identifiers.**
- The organisation is the repository key. A key that names no local `terraformbackend`
  repository is not an organisation.
- A workspace's ID is `<repository key>__<workspace name>`, as 7.x answers (RTFACT-30122). To
  read an ID, artifactweave splits it at each `__` from the left and takes the first split
  whose left part is a local `terraformbackend` repository holding a workspace of the right
  part's name **[I]**.
- A state version's ID is `<workspace ID>__<ms>`, where `<ms>` is the epoch milliseconds at
  which the server created the state version, unique within the workspace (one more than the
  newest when they would clash). Terraform holds a state-version ID only within one command
  (research 32 section 1.5), so an ID need stay valid only while its state is stored.
- A workspace name is one path segment that go-tfe sends: no `/`, no white space, not `.` or
  `..`, and a name the item service accepts. Anything else answers 422. TFE's narrower rule
  (letters, digits, `-` and `_`) is not applied, so names that work on 7.x keep working
  **[I]**.

**Routes.** All under `<tfe>`, all JSON:API. Permissions are on paths in the repository the
organisation names; `<ws>` is the workspace folder, `workspaces/<name>`.

| Method and path | Permission | Answers |
|---|---|---|
| `GET ping` | none | 204, no body, none of the `TFP-*` or `X-RateLimit-*` headers |
| `GET account/details` | an authenticated caller | 200 `users`: `id` and `username`, the caller's user name; 401 without a valid token |
| `GET organizations/<repo>` | Read on some path in the repository | 200 `organizations` (`id` and `name`: the key); 404 |
| `GET organizations/<repo>/entitlement-set` | Read on some path in the repository | 200 `entitlement-sets`: `operations` and every other entitlement `false`, `state-storage` `true`; 404 |
| `GET organizations/<repo>/workspaces` | Read on some path in the repository | 200, the workspaces the caller can read whose name contains `search[name]`, by name, paged by `page[number]` and `page[size]` (20 by default, at most 100) with `meta.pagination` |
| `POST organizations/<repo>/workspaces` | Deploy on `<ws>/workspace.json` | 201 the workspace; 422 for a name taken or refused; 400 for a body that is not a `workspaces` document |
| `GET organizations/<repo>/workspaces/<name>` | Read on `<ws>` | 200; 404 |
| `DELETE organizations/<repo>/workspaces/<name>` | Delete on `<ws>` | 204; 404; 409 while another user holds the lock |
| `GET workspaces/<id>`, `DELETE workspaces/<id>` | as the two rows above | as above; served for API callers, Terraform does not call them |
| `POST workspaces/<id>/actions/lock` | Read on `<ws>` | 200 the workspace, `locked` true; 409 when locked |
| `POST workspaces/<id>/actions/unlock` | Read on `<ws>` | 200; 409 when unlocked; 409 when another user holds the lock |
| `POST workspaces/<id>/actions/force-unlock` | Delete on `<ws>` | 200; 409 when unlocked |
| `GET workspaces/<id>/current-state-version` | Read on `<ws>` | 200 the state version; 404 when there is no state |
| `POST workspaces/<id>/state-versions` | Deploy on `<ws>/state.latest.json` | 201; 409 while another user holds the lock; 422 for a missing `md5` or `serial`, a `state` that is not base64 of a state, or one whose MD5 or serial differs from the attributes; 413 above the body limit |
| `PUT state-versions/<sv>/content?token=...`, the upload URL | the token | 200; 403 for a token that does not verify or has expired; 409 when the state version is already written or another user holds the lock; 422 when the body's MD5 or serial differs from the create's |
| `GET state-versions/<sv>` | Read on `<ws>` | 200; 404 |
| `GET state-versions/<sv>/download`, the download URL | Read on `<ws>` | 200 the plain state, `application/json`; 404 |
| `GET state-versions?filter[organization][name]=&filter[workspace][name]=` | Read on `<ws>` | 200, newest first, paged as the workspace list; 400 without both filters |
| any other method or path under `<tfe>` | | 404 |

**Documents.** Every resource carries `id` and `type`, since go-tfe refuses a document of
another type. Times are ISO 8601.
- A workspace: `name`, `locked`, `execution-mode` `"local"`, `operations` `false`,
  `terraform-version` (the value the create sent, or `"latest"` when it sent none),
  `created-at`, `updated-at`, and the relationship `organization`. Terraform reads `id`,
  `name`, `terraform-version` and `execution-mode`; `"local"` turns off its version checks
  and its variable reads, so a workspace created by one Terraform version works with every
  other (research 32 section 1.7).
- A state version: `serial`, `status` (`"finalized"`, or `"pending"` with
  `hosted-state-upload-url` in the answer to a state-less create), `size`, `created-at` and
  `hosted-state-download-url`, both URLs absolute on the request's base URL.

**Errors.** In JSON:API error objects with `Content-Type: application/vnd.api+json`:
`{"errors":[{"status":"409","title":"Conflict","detail":"<message>"}]}`, the status as a
string, the title the HTTP reason phrase, the detail artifactweave's message. go-tfe shows
the title and detail (research 32 section 1.4). The details that drive the client:
- an unlock refused because another user holds the lock says "Workspace <name> is locked by
  User <holder>", which go-tfe turns into "unable to unlock workspace locked by user";
- an unlock of an unlocked workspace says "Workspace <name> is not locked";
- a refused lock says who holds it, for API callers; Terraform does not read it.

A request without a token, or with an invalid or expired one, answers 401, as every API does
(decision [0005](0005-authentication-and-tokens.md)); an authenticated caller without the
permission answers 403, as Artifactory's native model does (decision
[0006](0006-authorization-native-permission-model.md)). A missing repository, workspace or
state version answers 404.

**Storage.** Items under the workspace folder, as 7.x keeps them; migration 0019 is not used.

| Item | Holds |
|---|---|
| `workspaces/<ws>/` | the workspace folder; while locked, the properties `locked.by` (the user, as 7.x) and `locked.at` (epoch milliseconds) |
| `workspaces/<ws>/workspace.json` | the workspace record: `name`, `id`, `terraform-version`, `created`, `createdBy`. Never sealed. The workspace exists while this item does **[I]** |
| `workspaces/<ws>/state.latest.json` | the current state |
| `workspaces/<ws>/state.<ms>.json` | each earlier state, named by its state version's `<ms>` |

- **A state write** is one transaction: `state.latest.json`, if any, moves to
  `state.<its ms>.json`, the new state becomes `state.latest.json`, and the package index
  follows. The server makes the move, so a caller with Deploy needs no Delete for it.
- **The latest pointer** is the name `state.latest.json`, as in 7.x. The package index
  caches what the API answers without reading the content.
- **The package index** (decision 0018): the package is the workspace; each state is a
  version named by its `<ms>`, ordered by number, whose one file is the state file; the
  version's metadata is the state's serial, lineage, MD5 and size. The current version is the
  one at `state.latest.json`. A reindex rebuilds it from the items, unsealing to read the
  serial and lineage and taking `state.latest.json`'s `<ms>` from its creation time. Cleanup
  sees workspaces as packages and states as versions, with the lead artifact `.json` that
  7.x names, and keeps the current version, as 7.x's "latest version is always excluded".
- **Writes from Terraform:**
  - With `state` (Terraform 1.0 to 1.5): the base64 is decoded, its MD5 must equal `md5` and
    the state's own `serial` must equal `serial`, as TFE requires, and the state is written:
    201 with `status` `"finalized"`.
  - Without `state` (Terraform 1.6 and later, and OpenTofu): 201 with `status` `"pending"`
    and the upload URL `<base><tfe>state-versions/<sv>/content?token=<token>`. Nothing is
    stored. The token is an HMAC, under a key derived from the master key, of the state
    version ID, the caller, `md5`, `serial` and an expiry ten minutes on **[I]**. The PUT,
    which carries no `Authorization` (research 32 section 1.6), is authorised by the token: it
    checks the body's MD5 and serial against the token's, that the caller still has Deploy,
    the lock rule below, and that the state version is not already written, then writes as
    the inline form does, as that caller.
  - `force`, `lineage`, `json-state-outputs`, `json-state` and `run` are accepted. The server
    does not compare serial or lineage with the current state: Terraform does, and `-force`
    exists to skip it. Lineage is kept in the index.
  - **The lock rule:** a write is refused with 409 while another user holds the lock. A write
    by the holder, or to an unlocked workspace (`terraform apply -lock=false`), is accepted.
  - No pending state version is recorded, so an unlock never waits for one.
- **Size.** No limit of its own. A create with inline `state` is a JSON body under the 64 MiB
  limit of JSON management bodies, 413 above it in the JSON:API envelope; the upload PUT
  streams the raw state to the blob store under the item service's limits.

**`encryptStates`.**
- When it is `true` at the time of a write, the state file's content is sealed: a `crypt`
  envelope (decision 0017: AES-256-GCM under the master key's current version, with the key
  version and key id in the header), purpose `terraform-state`, row
  `<repository key>/workspaces/<ws>`. The sealed bytes are the item's blob, so the blob store
  and the item model are unchanged. `workspace.json` is never sealed.
- The item records the sealed bytes' size and checksums. The item routes, AQL, the trash can
  and replication see and carry the sealed bytes, and a download through
  `/artifactory/<repo>/workspaces/...` serves them as stored, so a state is never readable
  there **[I]**. The TFE download URL unseals.
- Each seal has a random nonce, so two identical states give two blobs and a checksum reveals
  nothing of the content.
- A read tells a sealed file from a plain one by the envelope's header, which no state JSON
  starts with. `encryptStates` may therefore change on a repository update or a descriptor
  PATCH: a change applies to later writes, and files keep the form they were written in. It
  defaults to `true` at create, and an update that leaves it out keeps it.
- Losing the master key loses every sealed state, as 7.x warns. A state sealed under an old
  key version stays readable while that version is kept; `artifactweave keys rotate`
  (decision 0017) reseals state blobs before an old version can be retired.
- Sealed states copied to another instance cannot be opened there, which is why 7.x requires
  `encryptStates` off to replicate a backend repository (7.77); the operator guide says so.

**Locking.**
- The lock record is the folder's `locked.by` and `locked.at`, set and checked in one
  transaction, so of two concurrent locks one holds and the other answers 409. It survives a
  restart. Terraform's lock ID and operation never reach the server (research 32 section
  1.7); the reason it sends is not kept.
- `lock` needs Read, so a reader can `plan`, which locks. It answers 409 when the workspace is
  locked, by anyone.
- `unlock` releases the caller's own lock. Another user's lock answers 409 naming the holder.
- `force-unlock` needs Delete on the workspace and releases any lock.
- A second `plan` while the workspace is locked gets 409, and Terraform prints "Error
  acquiring the state lock ... workspace already locked (lock ID: "<repo>/<ws>")" with its
  own lock information (research 32 section 1.7). The holder shows as `locked.by` in the
  item's properties and in the 409's detail.
- The properties API refuses to set or delete `locked.by` and `locked.at` in a
  `terraformbackend` repository, with 400, so the lock has one writer; `force-unlock` is the
  way to clear it **[I]**.
- Deleting a workspace that another user has locked answers 409.

**`terraform login` until #262.** While the discovery document has `tfe.v2` and no
`login.v1`, `terraform login <host>` takes Terraform Enterprise's path (research 32 section
1.9): it opens `https://<host>/app/settings/tokens`, which artifactweave does not serve,
asks for a token to paste, checks it with `GET <tfe>account/details`, and stores it.
artifactweave serves that one route, so the interim `terraform login` stores an artifactweave
access token the user created through the Access API or the JFrog CLI, after checking that it
is valid: "Retrieved token for user <name>", or "Token is invalid: unauthorized" for a token
that is not. The tokens page stays a 404. Once #262 adds `login.v1`, Terraform uses it
instead and never calls `account/details`; the route stays, harmless. #262 decides whether
`/app/settings/tokens` should lead to its approval page.

**Permissions**, summarised:

| Operation | Needs |
|---|---|
| `init`, `workspace list`, `workspace select`, `state pull`, `plan`, a lock and the holder's unlock | Read |
| `workspace new`, `init` with `workspaces { name }` the first time, `apply`, `destroy`, `state push` | Deploy (and Read) |
| `workspace delete`, `force-unlock` (of anyone's lock) | Delete (and Read) |
| Manage | nothing in this API |

**The item routes.**
- A deploy, a checksum deploy, a folder create, and a copy or move whose target is in a
  `terraformbackend` repository answer 400 in the Artifactory envelope: "Repository '<key>'
  is a Terraform backend repository; its content is written only through the Terraform
  backend API" **[I]**. Such a write would pass the lock and the seal. Replication and
  federation between artifactweave instances are not client writes and are not refused.
- Deletes are allowed with Delete, into the trash can, as 7.x allows **[I]**. Deleting
  `state.latest.json` leaves the workspace with no current state, so Terraform then sees an
  empty workspace; a restore from the trash can puts it back. Deleting `workspace.json` or
  the folder deletes the workspace. A move out of the repository is a delete here. The
  package index follows through the format hooks.
- Listings, item info, the storage API, AQL and property searches see the files, their
  sizes and checksums, and the lock properties.
- `blackedOut`, the include and exclude patterns, `downloadRedirect` and CDN download do not
  apply to the backend API. 7.x offers neither the blackout nor CDN download for Terraform BE,
  and the download URL must unseal, so it cannot redirect; the patterns are left out
  **[I]**.

## Rationale

- **Items, not tables.** 7.x keeps states as items, and its documentation, blog and logs
  name the paths. As items they reach the storage API, AQL, the trash can, cleanup and
  archive (which cover Terraform BE from 7.104.5), replication and the storage summary
  without code of their own. A table would hold nothing an item cannot: the lock is a
  property in 7.x, and the metadata the API answers is the package index's cache, rebuilt
  from the items.
- **The upload URL and inline state both.** Terraform 1.0 to 1.5 send the state inline and
  1.6 and later ask for an upload URL. The fallback message, "param is missing or the value
  is empty: state", would also make 1.6 send it inline, but it costs a refused request per
  write and keeps the state as base64 in a JSON body, which is what failed on 7.x above 20 MB
  (RTFACT-29974). The upload streams the raw state. The token makes the URL stateless and
  short-lived, as the multipart part URLs are signed, so no pending record is needed.
- **No pending state.** TFE blocks an unlock until a pending state version is uploaded, and
  Terraform retries that unlock with no deadline. Terraform uploads before it unlocks, and
  keeps the lock itself when an upload fails, so the block adds nothing but a way to hang a
  client.
- **`execution-mode: "local"` and `operations: false`.** Terraform 1.16.6, run against a
  server answering so, passed every command whatever version the workspace recorded; without
  it `state push` failed on the recorded version, which matches RTFACT-29616 (research 32
  section 1.7).
- **403 for a forbidden request.** TFE answers 404 to hide a resource, and go-tfe reads a 404
  on the workspace as "create it", so a caller who may not read the workspace would be told
  that a create failed instead of why. 403 is what Artifactory answers and what
  RTFACT-27352's log shows.
- **401 for an expired token.** RTFACT-27352's 403 came from requests falling back to the
  anonymous user before the token expired, known issue RTDEV-27518, fixed in 7.46.3. The
  intended behaviour is the token's own.
- **No 20 MB limit.** That was the JSON parser's default string length, and JFrog fixed it in
  7.68.11.
- **Lock with Read.** `terraform plan` locks, so a reader who may plan must be able to lock.
  A reader cannot release another's lock, and Delete clears a lock left behind.
- **Writes on an unlocked workspace.** TFE requires the lock. JFrog's blog says only that
  other users are denied while a workspace is locked, and Terraform documents `-lock=false`;
  refusing it would break pipelines that work on 7.x **[I]**.
- **A sealed blob, not a sealed column.** The state is a file the item routes, the trash can
  and replication move; sealing the blob keeps the item model and the content-addressed store
  as they are. The checksums of the stored bytes keep every download verifiable. The row in
  the additional data ties a state to its workspace, which survives the rename to
  `state.<ms>.json` and a restore from the trash can.
- **`encryptStates` may change.** JFrog's replication note has users turn the flag off on an
  existing repository. Reading by the envelope's header makes a change safe without
  rewriting stored states.
- **Generic writes refused.** A file put through the item routes would bypass the lock and the
  seal and could replace `state.latest.json` under a running apply. Deleting stays allowed,
  as for any item, and the trash can undoes it.
- **`account/details` for the interim `terraform login`.** `tfe.v2` is in 7.x's discovery
  document, and leaving it out would not avoid the fallback: with `tfe.v2.1` present and
  `tfe.v2` absent, `terraform login` stops with "requires a newer version of Terraform CLI",
  a wrong message. Without the route the fallback ends in "Failed to retrieve user account
  details: resource not found" after sending the user to a page that does not exist. With
  it, the fallback becomes a working login that checks and stores the token a user would
  otherwise write into `credentials.tfrc.json` by hand. 7.x never reaches this path, since
  it serves `login.v1`; whether it serves `account/details` is **[I]**.
- **No deviation registered.** Where this decision differs from TFE, Artifactory, not TFE, is
  the contract. Where 7.x is undocumented the point is **[I]** and on
  [#216](https://github.com/weaveplatform/artifactweave/issues/216).

**Alternatives considered:**
- *A table for workspaces and state versions (migration 0019):* a second source of truth
  beside the items, which the format hooks would have to keep in step on every delete,
  restore and move.
- *Answer the fallback message instead of an upload URL:* simpler, with the costs above.
- *A sealed column holding the state:* the item routes and the trash can would see no
  content, and the item model would need items without blobs.
- *Refuse `encryptStates` changes after create:* would refuse what JFrog's replication note
  asks users to do.
- *Guard the login fallback* by answering `account/details` with an error whose detail says
  to put a token in `credentials.tfrc.json`: it explains, but refuses a login that can work.

## Artifactory 7.x check

The decision stands against 7.161.32 (SaaS 7.174.0). Confirmed by 7.x sources (research 32
section 9): local repositories only; `encryptStates` true by default under the master key;
`backend "remote"` with the repository key as `organization`; the four discovery keys; the
`<repo>__<ws>` workspace ID; workspaces under `workspaces/<ws>/` with `state.latest.json`,
timestamped earlier states and `workspace.json`; `locked.by`; replication needing
`encryptStates` off (7.77); cleanup and archive for Terraform BE (7.104.5, SaaS 7.103.0).
Two 7.x bugs are not reproduced: the 20,000,000-character limit (fixed in 7.68.11) and the
anonymous fallback before a token expires (fixed in 7.46.3). No release from 7.84 to 7.161.32
or SaaS 7.80 to 7.174.0 changes the backend otherwise.

## Constraints

- Research 32 section 6 lists what is unconfirmed until the live comparison (P19, #216). The
  points that would change this decision are which upload form 7.x answers, its error bodies
  and statuses, the permissions each operation needs, whether it refuses item-route writes
  and writes without the lock, and what an item download of an encrypted state serves.
- `keys rotate` must reseal state blobs before an old key version is retired; until it does,
  rotation keeps the old version.
- The `cloud` block discovers `tfe.v2` and is not served; it fails at the first call it needs
  that this API does not answer.
- One process (decision [0004](0004-storage-sql-metadata-and-blobs.md)): the lock's
  transaction is the store's.

## Implementation notes (#558)

The API is mounted at the route forms above by `internal/server/terraformbackend`.
The storage service remains the only workspace, lock and state writer. Its sealed
state is one AEAD message, so writes and downloads buffer that message for validation;
raw PUTs have no backend size cap, while inline JSON creates retain the 64 MiB
management limit. A 21 MiB raw state and a JSON overflow have regression coverage.

Upload capabilities preserve the issuing access token's scope and expire no later
than that token or ten minutes, whichever comes first. Each PUT rechecks the caller's enabled status and Deploy permission, the workspace record identity and creation timestamp, the
state digest and serial, and the transaction's lock and one-write checks. The workspace
record is checked again inside the write transaction after the upload body is read.
Deleting and recreating a workspace cannot reuse an old capability, including during
an upload or within the same millisecond. Transient and
synthetic token identities retain their access-token semantics; a capability issued
for a stored user cannot become a transient grant after that user is deleted.
Capability requests bypass
normal authentication only on the exact PUT content route; registry `sig` parameters
cannot bypass backend authentication. State documents and downloads are not cached.

## Verification

- #557: the storage, the index and the lock on every store; two concurrent locks yield one
  holder; force-unlock needs Delete; a sealed state round-trips and its item download serves
  the envelope; `encryptStates` off serves the plain state; a trash restore of a workspace.
- #558: `TestRemoteBackendSequence`, `TestUploadCapability`,
  `TestConcurrentLocksAndUploads` and `TestBodyLimitsAndDisconnect`; permanent
  `terraform_backend_api.feature`; status and route-by-principal rows for every route, `account/details` with a valid,
  an expired and no token; a server test replaying research 32 section 1.5's sequences for
  `init`, `workspace new`, `plan`, `apply`, `state pull`, `state push`, a second `plan` while
  locked, `force-unlock` and `workspace delete`, with both state-version forms.
- #559: `terraform` with the `remote` backend against a `terraformbackend` repository in
  acceptance.

## References

- [32-terraform-backend](../32-terraform-backend.md), [17-undocumented-shapes](../17-undocumented-shapes.md#terraform-discovery-and-the-remote-backend),
  [04-package-protocols](../04-package-protocols.md#state-backend)
- [0017-sealed-columns-and-master-key](0017-sealed-columns-and-master-key.md),
  [0018-package-index](0018-package-index.md),
  [0032-package-format-handlers](0032-package-format-handlers.md),
  [0005-authentication-and-tokens](0005-authentication-and-tokens.md),
  [0006-authorization-native-permission-model](0006-authorization-native-permission-model.md)
- hashicorp/terraform v1.16.6 `internal/backend/remote/`; hashicorp/go-tfe v1.110.0 `v1.go`
- HashiCorp's TFE API documentation, `hashicorp/web-unified-docs` `f17b63b7b01c`:
  `api-docs/workspaces.mdx`, `api-docs/state-versions.mdx`
