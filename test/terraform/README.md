# Terraform scenario driver

`run.sh` applies the app-layer references in
[deploy/artifactweave/terraform](../../deploy/artifactweave/terraform) against a running
artifactweave, with the real `jfrog/artifactory` and `jfrog/platform` providers. The
scenarios, and how to apply one to your own instance, are described in
[Managing artifactweave with Terraform](../../deploy/artifactweave/README.md). This directory
holds only the driver.

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

The remote-repositories scenario covers all fourteen P9 provider resource types. It applies
two revisions, requires an empty refreshed plan after each, checks API configuration and
secret redaction, checks that persistent data contains no plaintext fixture credentials,
and destroys the resources. It seeds/removes its proxy through the YAML descriptor;
provider `artifactory_proxy` CRUD depends on the XML reads owned by P12/#303. Its endpoints belong to the controlled fixture server. It
proves management only; native content adapters have separate client acceptance scenarios.
