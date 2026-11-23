# Terraform scenario driver

`run.sh` applies the app-layer references in
[deploy/artifactweave/terraform](../../deploy/artifactweave/terraform) against a running
artifactweave, with the real `jfrog/artifactory`, `jfrog/platform` and `jfrog/project` providers. The
scenarios, and how to apply one to your own instance, are described in
[Managing artifactweave with Terraform](../../deploy/artifactweave/README.md). This directory
holds the driver and runtime assertion helpers; consumer Terraform stays under `deploy/`.

The `terraform` CI job runs every scenario in the Makefile's `TERRAFORM_SCENARIOS`, and so does
`make terraform` locally:

```sh
make terraform                       # init from the mirror and validate each scenario
make terraform TERRAFORM_MODE=apply  # also apply each, require an empty plan, destroy
./test/terraform/run.sh apply deploy/artifactweave/terraform/identity  # one scenario
```

`run.sh` installs the providers only from a filesystem mirror. It builds the mirror from the
pinned release zips, checks each against the SHA-256 in the script, and then against each
scenario's committed `.terraform.lock.hcl`. No run contacts registry.terraform.io.

The providers authenticate only with a Bearer access token (`access_token`). The apply run
starts one `-cover` artifactweave and mints an admin token from its token API with the
bootstrap admin's password, starting the server with `token.allow_basic_auth` on so that the
Access token API takes it. `TERRAFORM_ACCESS_TOKEN` supplies a token instead. Terraform runs
on a copy of each scenario in the work directory, so no state file is left in the source
tree. Each scenario has a case in `run.sh` with its apply steps (the repositories file deploy
and key pair association, the identity key pair); a scenario without one fails rather than
being skipped. The repositories scenario's GPG key pair is made afresh on each run by
`tools/gpgkey`, with Go's OpenPGP library, so no host needs `gpg`. See
[docs/testing/ci.md](../../docs/testing/ci.md#terraform-scenarios) for the steps and for
moving to a new provider version.

The apply run starts the server with `api.budget_timeout` at its 5s maximum. Terraform deletes
up to ten resources at once, and on SQLite the default 500ms admission deadline can refuse one
of those requests with a 503 that the provider does not retry (#1147). A failed run prints the
end of the server log and retains its private work directory, including Terraform state.

The server receives an explicit private configuration for SQLite and filesystem storage;
inherited `ARTIFACTWEAVE_*` variables are removed only from that child. The caller's
environment and `HOME` remain intact. The child binds a private ephemeral loopback backend
and reports its address in its fresh JSON startup log. An owned TCP forwarder binds
`TERRAFORM_PORT` (default 18081). Before forwarding any client bytes on each connection,
it proves the connected backend peer socket belongs to the child: Linux uses the child's
`/proc` socket inode/endpoint records; macOS uses `/usr/sbin/lsof` established endpoints.
Missing ownership evidence fails closed. Each verified connection stays pinned to that
peer; it never reconnects or retries against a replacement listener. A connect or
ownership-proof failure quarantines its still-bound frontend, refuses new connections and records a private failure
marker until the parent's active client command finishes and cleanup reaps it. Provider
retries cannot race a released frontend.
Normal EOF/reset/broken pipe after peer verification closes only that fixed connection;
any subsequent connection still requires independent ownership proof. This also protects
native provider and Python checker reconnects while the child drains after shutdown.
Startup failure, an occupied frontend, missing listener/socket evidence or the ten-second
readiness deadline stops the run before token creation or provider effects. Fixture HTTP
bypasses host proxy settings. Cleanup reaps the owned forwarder before the child.

`TERRAFORM_WORK` names a retained parent directory. Each invocation creates a private
`run.*` child, prints its path, and keeps it on success or failure. This preserves earlier
evidence and prevents a later instance from inheriting an earlier run's state, configuration
or listening event. Without that option, successful temporary workspaces are removed and
failed ones retained. Cleanup signals and reaps only the forwarder and child it started.

Callers continuing after validation consume the single `Terraform provider workspace: `
output line and use that fresh child for both `scenarios/<scenario>` and `terraformrc`.
The two-instance replication acceptance caller verifies it is a new private direct `run.*`
child with contained configuration/scenario paths. Its `provider.log` and sanitized evidence
remain in the caller-selected parent; earlier child state is preserved rather than resumed.
The existing Linux provider CI step runs its isolated workspace contracts alongside the
required `@replication_provider` two-backend scenarios. The native harness keeps its
existing platform ownership rather than joining the three-OS package-unit matrix.

`python3 -I test/terraform/driver_test.py` exercises the shell entry point with owned
ephemeral listeners, a Go/chi HTTP fixture built once, and stub Go/Terraform clients. The
draining-child cases hold an active request, signal shutdown and bind an owned replacement
spy; neither authentication nor provider reconnects may send it request bytes.
`make deploy-check` runs the same
regressions; the pinned provider lane supplies real apply/plan/destroy evidence.

The remote-repositories scenario covers all fourteen P9 provider resource types. It applies
two revisions, requires an empty refreshed plan after each, checks API configuration and
secret redaction, checks that persistent data contains no plaintext fixture credentials,
and destroys the resources. It seeds/removes its proxy through the YAML descriptor;
provider `artifactory_proxy` CRUD depends on the XML reads owned by P12/#303. Its endpoints belong to the controlled fixture server. It
proves management only; native content adapters have separate client acceptance scenarios.

The required `configuration` reference in `deploy/artifactweave/terraform` manages only pinned
`artifactory_proxy` and `artifactory_property_set` resources. It creates, updates,
refreshes, requires empty plans, clears the optional proxy password, and destroys
both resources on the driver's own temporary server and state. Actual server passwords and
sealed-row presence/absence are checked with `tools/configurationproof`; Terraform state
alone cannot prove password preservation or clearing. XML descriptor GET
is a required dependency; missing XML fails conformance. The full seven-resource
P12 qualification remains #1837.

The required `trashcan` reference applies pinned `artifactory_trashcan_config` in two
revisions (off with 3 days, on with 0 days), and after each requires an empty refreshed plan
and runs `check-trashcan.py`. That script reads the XML descriptor back and checks what a
delete then does: final with the trash can off, and with it on kept until a collection pass
purges it at 0 days. The scenario then imports the singleton into empty state and requires
an empty plan. Destroy must leave the provider's documented reset, on with 14 days.

The virtual-repositories scenario applies all 33 provider virtual resources, each over a local
and a remote member, with every virtual field set away from its default. It applies two
revisions, the second reversing every member list, and after each requires an empty plan and
reads each virtual's member order back through the repositories API. The provider posts a
usage report (`POST /artifactory/api/system/usage`) for each of its 102 resources' operations;
those are ordinary requests ([decision 0043](../../docs/research/decisions/0043-api-reliability.md#classes-moved-since)),
so the run keeps the default request rates.
