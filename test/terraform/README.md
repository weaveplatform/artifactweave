# Terraform fixture

Applies the real `jfrog/artifactory` provider against a running artifactweave. Used by
the `terraform` CI job from P2 and runnable locally:

```sh
go build -o /tmp/artifactweave ./cmd/artifactweave
ARTIFACTWEAVE_MASTER_KEY=local /tmp/artifactweave serve --listen 127.0.0.1:18081 --data-dir /tmp/aw &
cd test/terraform
terraform init
terraform plan -var url=http://127.0.0.1:18081 -var access_token=dummy
```

Until P2 lands the plan stops at the `artifactory_repositories` data read with the
Artifactory 404 envelope; the provider's configure step (version and usage calls) has
already succeeded at that point. The provider mirror used by CI is populated with
`terraform providers mirror` so the job does not depend on registry.terraform.io.
