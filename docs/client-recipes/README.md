# Connect a package client

Choose a format from the [recipe index](index.md). Each recipe names its
repository URL, tested client version, authentication mechanism and supported
publish/install operations. Examples target an existing repository to which
you have access. Creating a repository is an administrator's separate task.

## Common setup

Use a disposable project directory and the fresh client state described in
your recipe. Populate these environment variables through your shell or secret
manager before running its commands:

| Variable | Meaning |
|---|---|
| `AW_URL` | Deployment origin, such as `https://packages.example.com`, without `/artifactory` or a trailing slash |
| `AW_REPO` | Existing repository key, such as `npm-local` |
| `AW_USER` | User whose permissions authorize the requested operation |
| `AW_TOKEN` | That user's short-lived access or reference token, provided at runtime |

Keep the hostname consistent with the configured public base URL. Registry
clients and download metadata can use absolute URLs. Use TLS with a trusted
certificate; the acceptance harness's private certificates and HTTP exceptions
are test fixtures, not deployment instructions.

`${AW_URL}` and `${AW_REPO}` in shell blocks are shell variables. In configuration
formats that do not expand shell variables, replace the placeholders before
saving, or use the recipe's command that writes the file. Any additional package
coordinates, file paths, workspace names or hostnames are identified by that
recipe. Build your package before running a publication command.

The supported classes beside each operation matter:

- **Local:** publish and consume stored packages where the format supports it.
- **Remote:** consume through the configured upstream and cache. Do not publish
  to a remote or its cache alias.
- **Virtual:** consume through the configured member ordering. Recipes publish
  to a writable member unless a native virtual-deployment flow has its own proof.
- **Federated:** the listed commands have one-node evidence. Cross-vendor
  federation is a separate capability.

Do not select a class absent from a recipe. A registered repository class alone
does not prove that every native operation is available. The
[format inventory](../implementation/package-format-matrix.md) and each recipe's
limits record the distinction. Release bundles remain planned and have no
native connection recipe.

## Credentials and authorization

Use your own token; the examples contain no reusable administrator credential.
For Basic-only clients, artifactweave accepts an access/reference token as the
password **when the username matches the token's subject**. Bearer clients use
the token directly. This is enforced by [the authenticator](../../internal/auth/token.go).
Client-specific API-key, host-matching or credential-store rules still apply.

A token never creates repository permissions. The subject must have read access
to the selected repository and package paths; publication additionally needs
deploy/write access, and overwrite/delete requires its corresponding permission.
`applied-permissions/user` retains the subject's current grants. Resource scopes
such as `artifact:npm-local/**:r` narrow those grants; they do not bypass
permission targets, project boundaries or API restrictions. Use the existing
[Access token API](../../api/access.openapi.yaml) or your approved credential
flow to obtain a token, and consult the
[authorization contract](../research/decisions/0006-authorization-native-permission-model.md)
before narrowing a token used by a client that reads several metadata paths.

Some IDEs do not provide the credential hook needed by their native repository
client. Their recipes explicitly require an administrator-approved anonymous
**read** grant on the intended repository. They do not imply anonymous access
is enabled by default, and they never require anonymous publication.

Avoid recording expanded secrets in shell history, tracing (`set -x`), logs,
source control or copied support transcripts. Use `umask 077` before creating
temporary credential files. Where a client writes a token or a credential URL
into its configuration, protect that directory and remove it when finished.
URL credentials must be percent-encoded; the corresponding recipe explains
this when its tested client requires them. Do not send artifactweave credentials
to an upstream registry, unrelated host or public package index.

An authentication failure is not fixed by changing the repository type. A 401
usually means credentials were absent or invalid. A 403 can indicate permissions,
token scope or an API restriction. For 429/503, respect `Retry-After` and the
[API retry guidance](../research/api-reliability/retrying.md); do not blindly
repeat a publication whose outcome is uncertain.

## Verification

The reusable sources are [src/](src/), one [schema-v1 JSON object](schema.md) per
implemented format. `make client-recipes` validates them and renders the index and guides.
`GOWORK=off go run ./tools/clientrecipes -check` checks that the committed guides
and [evidence fingerprints](evidence-lock.json) match their sources. CI performs
this check even for documentation changes, together with shell/Python syntax
checks that never execute the published commands. A client pin or cited test change
requires reviewing the affected recipe before regenerating.

The check catches missing formats, invalid class claims, missing instructions,
stale source anchors and generated-output drift. It does **not** replace running
native clients. Verification uses the existing acceptance harness and its pinned
client images/actions, with fresh client homes and controlled repositories:

```sh
GOWORK=off go test ./tools/clientrecipes ./tools/formatinventory
python3 tools/clientrecipes/check_snippets.py
# Example after installing/building the clients required by these tags:
ARTIFACTWEAVE_ACCEPT_TAGS='@ansible || @apm' GOWORK=off \
  go test ./test/acceptance -run '^TestAccFeatures$' -count=1 -timeout=20m
```

On macOS the harness requires `ARTIFACTWEAVE_ACCEPT_LOCAL=1` and a Docker engine
able to run the pinned Linux clients. The full Linux CI gate installs the pinned
clients automatically. Successful generic uploads or repository CRUD are not
substitutes for native install/resolve evidence.

When updating a recipe, retain the actual client version, exercised native URL,
successful install/publish result, invalid-credential or permission-denial result,
and the exact tested commit. Use the format's linked feature and step sources to
inspect client-home isolation and request-trace assertions. Do not label a
protocol-only fixture as a native-client run. Client limitations are retained in
the corresponding guide rather than hidden by an untested command.

[QUAL-01 #792](https://github.com/weaveplatform/artifactweave/issues/792) owns the
final run of all 28 P9c formats on merged main, comparison with current JFrog
connection guides, client/image digests and combined coverage evidence. A run
on an implementation branch does not satisfy that merged-main qualification.

## P39 Set Me Up handoff

The browser console consumes the same schema-v1 recipe sources. It should:

1. Discover repositories visible to the signed-in user through the existing
   APIs. Select the format, repository class and client before showing commands.
2. Filter each action by its recipe's classes and current server permissions.
   Show the explanatory note for unavailable actions; never render a misleading
   publish command for remote repositories or a planned format.
3. Offer the existing token flow or a user-supplied token, preserving subject,
   expiry and scopes. P21's browser session/CSRF rules apply to token creation.
4. Render values as data with escaping appropriate to the shell or file format.
   The JSON contains reviewed templates, not a shell program for the server to
   execute. A repository name, URL or token must never become executable code.
5. Keep tokens out of analytics, URLs, persistent page state, server logs and
   recipe sources. Reveal/copy credentials only by explicit user action; clear
   them when the dialog closes. Explain any native client that persists them.
6. Show the pinned tested version, limits and evidence with Configure, Publish
   and Install actions. Preserve documented anonymous-read prerequisites rather
   than substituting an unsupported token setting.

This ticket provides recipe data and documentation, not the browser console or
a new configuration API. The flow follows the separation described by
[JFrog's client connection guide](https://docs.jfrog.com/artifactory/docs/connect-your-package-manager-to-artifactory)
and [Set Me Up guide](https://docs.jfrog.com/artifactory/docs/use-artifactory-set-me-up-for-configuring-package-manager-clients),
reviewed 2026-11-03. Those guides describe the upstream UI; artifactweave's
implemented endpoints and retained native tests determine which commands are
offered here.
