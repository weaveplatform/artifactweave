# CI-installed native clients

This is the Phase 9c inventory of clients installed on the CI runner rather
than in an acceptance Dockerfile. Versions were checked against the linked
upstream release feeds on 2026-10-27. The workflow verifies the direct client
archive or wheel before installation and prints its version where the client
offers a version command. Dockerfile clients are inventoried separately in
the client image inventory tracked by #852.

| Client and CI setup | Current version | Release and digest source | Architecture and consuming evidence |
|---|---|---|---|
| [crane](../../.github/actions/registry-clients/action.yml) | 0.22.1 | [go-containerregistry releases](https://github.com/google/go-containerregistry/releases/tag/v0.22.1), release asset SHA-256 | Linux amd64; `@docker`, `@oci` |
| [ORAS](../../.github/actions/registry-clients/action.yml) | 1.3.6 | [ORAS release](https://github.com/oras-project/oras/releases/tag/v1.3.6), release asset SHA-256 | Linux amd64; `@oci` |
| [Cosign](../../.github/actions/registry-clients/action.yml) | 3.1.4 | [Cosign release](https://github.com/sigstore/cosign/releases/tag/v3.1.4), release asset SHA-256 | Linux amd64; `@oci` signatures |
| [Helm](../../.github/actions/registry-clients/action.yml) | 4.3.1 | [Helm release](https://github.com/helm/helm/releases/tag/v4.3.1), official archive SHA-256 | Linux amd64; `@helm` |
| [Terraform](../../deploy/check.sh) | 1.17.0 | [HashiCorp release](https://releases.hashicorp.com/terraform/1.17.0/), published SHA256SUMS | Linux and macOS, amd64 and arm64; `@terraform`, provider conformance, deployment and platform scenarios |
| [Terraform null provider](../../.github/actions/registry-clients/action.yml) | 3.3.2 | [HashiCorp release](https://releases.hashicorp.com/terraform-provider-null/3.3.2/), published SHA256SUMS | Linux amd64; `@terraform_providers` |
| [JFrog CLI](../../.github/actions/jfrog-cli/action.yml) | 2.128.0 | [JFrog CLI release](https://github.com/jfrog/jfrog-cli/releases/tag/v2.128.0), [JFrog Artifactory storage checksum](https://releases.jfrog.io/artifactory/api/storage/jfrog-cli/v2-jf/2.128.0/jfrog-cli-linux-amd64/jf) | Linux amd64; `@jf`, NuGet and platform scenarios |
| [Agent Package Manager](../../.github/actions/apm-client/action.yml) | 0.35.0 | [APM release](https://github.com/microsoft/apm/releases/tag/v0.35.0), release asset SHA-256 | Linux amd64; `@agentpackages` |
| [Hex Editor VSIX](../../.github/actions/ai-editor-assets/action.yml) | 1.11.1 | [extension changelog](https://github.com/microsoft/vscode-hexeditor/blob/main/CHANGELOG.md), Marketplace manifest, VSIX and signature SHA-256 | VS Code Marketplace assets; `@aieditor` |
| [Hugging Face Hub and hf-xet](../../.github/workflows/quality-gate.yml) | 2.3.0 and 1.7.0 | [Hub files](https://pypi.org/project/huggingface-hub/2.3.0/#files), [Xet files](https://pypi.org/project/hf-xet/1.7.0/#files), PyPI wheel SHA-256 | Linux amd64, Python 3.12; `@huggingface` |
| [Conan 2 and Conan 1](../../.github/workflows/quality-gate.yml) | 2.33.0 and 1.66.0 | [Conan 2 files](https://pypi.org/project/conan/2.33.0/#files), [Conan 1 files](https://pypi.org/project/conan/1.66.0/#files), direct distribution SHA-256 | Linux amd64, Python 3.12; `@conan`; Conan 1 is the explicit legacy compatibility lane alongside Conan 2 |

The Artifactory and Platform provider conformance runner in
[`test/terraform/run.sh`](../../test/terraform/run.sh) pins the current JFrog
provider releases, 12.11.14 and 2.2.11, to their checked archives. Those are
provider fixtures rather than package managers. The remaining
[`deploy/check.sh`](../../deploy/check.sh) tools are deployment checks:
TFLint 0.65.0, Trivy 0.76.1 and terraform-docs 0.24.0. Their four-platform
archives are checksum verified too.

The current Hub client revealed a commit-response defect. [#858](https://github.com/weaveplatform/artifactweave/issues/858)
was fixed in merged [PR #859](https://github.com/weaveplatform/artifactweave/pull/859).
Hub 2.3 then passed the real model/dataset scenarios locally on merged main. The
current-client run on merged main belongs to [QUAL-01 #792](https://github.com/weaveplatform/artifactweave/issues/792).
