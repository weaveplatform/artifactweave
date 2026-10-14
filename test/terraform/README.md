# Terraform fixture

Applies the real `jfrog/artifactory` provider against a running artifactweave:
- a local generic repository (`artifactory_local_generic_repository`);
- properties on a file deployed into it (`artifactory_item_properties`).

The `terraform` CI job runs it, and so does `make terraform` locally:

```sh
make terraform                       # init from the mirror and validate
make terraform TERRAFORM_MODE=apply  # also apply, require an empty plan, destroy
```

`run.sh` installs the provider only from a filesystem mirror. It builds the mirror from the
pinned release zip, checks it against the SHA-256 in the script, and then checks it against
the committed `.terraform.lock.hcl`. No run contacts registry.terraform.io.

The provider authenticates only with a Bearer access token (`access_token`). The apply run
mints an admin token from the server's token API with the bootstrap admin's password.
`TERRAFORM_ACCESS_TOKEN` supplies a token instead. Terraform runs on a copy of this
directory in the work directory, so no state file is left here. See
[docs/testing/ci.md](../../docs/testing/ci.md#terraform-fixture) for the steps and for
moving to a new provider version.
