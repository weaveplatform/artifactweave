# Terraform scenarios

`run.sh` applies Terraform scenarios with the real `jfrog/artifactory` and `jfrog/platform`
providers against a running artifactweave. It is the driver for:
- the repositories fixture in this directory: a local generic repository
  (`artifactory_local_generic_repository`) and properties on a file deployed into it
  (`artifactory_item_properties`);
- the identity reference in
  [deploy/artifactweave/terraform/identity](../../deploy/artifactweave/terraform/identity):
  users, groups, a permission, a scoped token and a key pair;
- the OIDC reference in
  [deploy/artifactweave/terraform/oidc](../../deploy/artifactweave/terraform/oidc):
  two OIDC providers and their identity mappings.

The `terraform` CI job runs them all, and so does `make terraform` locally:

```sh
make terraform                       # init from the mirror and validate each scenario
make terraform TERRAFORM_MODE=apply  # also apply each, require an empty plan, destroy
```

`run.sh` installs the providers only from a filesystem mirror. It builds the mirror from the
pinned release zips, checks each against the SHA-256 in the script, and then against each
scenario's committed `.terraform.lock.hcl`. No run contacts registry.terraform.io.

The providers authenticate only with a Bearer access token (`access_token`). The apply run
mints an admin token from the server's token API with the bootstrap admin's password.
`TERRAFORM_ACCESS_TOKEN` supplies a token instead. Terraform runs on a copy of each scenario
in the work directory, so no state file is left in the source tree. See
[docs/testing/ci.md](../../docs/testing/ci.md#terraform-scenarios) for the steps and for
moving to a new provider version.
