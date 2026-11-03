# Phase 9c client release review

Reviewed against primary release feeds on **2026-11-03** for [#792](https://github.com/weaveplatform/artifactweave/issues/792). This is a dated release and identity review, not final execution qualification. The [combined ledger](phase9c-signoff.md) still requires a green full run on the final merged main SHA and the current release candidate.

The previous client inventories were based on the earlier #852/#853 work. New releases appeared before final phase acceptance. These primary pins were refreshed without changing protocol fixtures or expected package bytes:

| Client | Previous pin → selected release | Published | Primary release evidence |
|---|---|---|---|
| Ansible Core | 2.21.5 → 2.22.0 | Nov 2 | [PyPI release metadata](https://pypi.org/pypi/ansible-core/2.22.0/json) |
| FrogML | 2.1.153 → 3.0.0 | Nov 3 | [PyPI release metadata](https://pypi.org/pypi/frogml/3.0.0/json) |
| Hugging Face Hub | 2.3.0 → 2.4.0 | Oct 29 | [PyPI release metadata](https://pypi.org/pypi/huggingface-hub/2.4.0/json) |
| hf-xet | 1.7.0 → 1.8.0 | Oct 30 | [PyPI release metadata](https://pypi.org/pypi/hf-xet/1.8.0/json) |
| Conan primary | 2.33.0 → 2.34.0 | Oct 28 | [PyPI release metadata](https://pypi.org/pypi/conan/2.34.0/json) |
| Composer primary | 2.10.3 → 2.11.1 | Oct 30 | [Official changelog](https://getcomposer.org/changelog/2.11.1) |
| Elixir/Mix | 1.20.4 → 1.20.5 | Oct 29 | [Official release](https://github.com/elixir-lang/elixir/releases/tag/v1.20.5) |
| VS Code | commit `e646868…` → 1.144.1, commit `261c84cb1eddf11235f9e2c921425e9663fbe556` | Nov 1 | [Official stable API](https://update.code.visualstudio.com/api/update/linux-x64/stable/latest) |
| IntelliJ IDEA | 2025.3 → 2026.2.4 | Oct 30 | [Current release feed](https://data.services.jetbrains.com/products/releases?code=IIU&latest=true&type=release) |
| pip | 26.2.2 → 26.3 | Nov 2 | [PyPI release metadata](https://pypi.org/pypi/pip/26.3/json) |
| uv | 0.13.6 → 0.13.7 | Nov 3 | [PyPI release metadata](https://pypi.org/pypi/uv/0.13.7/json) |
| JFrog CLI | 2.128.0 → 2.131.0 | Nov 3 | [Official release](https://github.com/jfrog/jfrog-cli/releases/tag/v2.131.0) |
| Terraform | 1.17.0 → 1.17.1 | Oct 28 | [Official release](https://github.com/hashicorp/terraform/releases/tag/v1.17.1) |
| Fedora/DNF/RPM lane | Fedora 44 → 45 | Nov 3 | [Official stable download](https://fedoraproject.org/workstation/download/) |
| RubyGems / Bundler | bundled 4.0.20 → explicit 4.0.23 | Oct 14 | [RubyGems release metadata](https://rubygems.org/api/v2/rubygems/rubygems-update/versions/4.0.23.json), [Bundler release metadata](https://rubygems.org/api/v2/rubygems/bundler/versions/4.0.23.json) |

Ruby 4.0.9 itself remains current. Its [RubyGems source](https://raw.githubusercontent.com/ruby/ruby/v4.0.9/lib/rubygems.rb) and [Bundler source](https://raw.githubusercontent.com/ruby/ruby/v4.0.9/lib/bundler/version.rb) declare 4.0.20, and the old base image has no update layer. The Gems image therefore installs the current tools explicitly from checksum-verified gem files and prints their runtime versions. Both independent pins participate in `make deps-sync`, including download filenames, installation paths, recipe labels and current inventory.

The old IntelliJ `IIC` release feed stops at 2025.3. The current `IIU` feed represents the unified distribution, whose free core remains available. The inventory now cites that feed; an old Community label does not justify an obsolete primary client. [JetBrains distribution documentation](https://www.jetbrains.com/help/idea/intellij-idea-single-distribution.html)

## Runtime choices and compatibility

| Image | Selected runtime / image | Reason |
|---|---|---|
| Primary Python tools and Ansible | Python 3.15.2 | Current stable Python patch. Ansible 2.22 explicitly [adds Python 3.15 controller support](https://raw.githubusercontent.com/ansible/ansible/v2.22.0/changelogs/CHANGELOG-v2.22.rst), removing the older client's compatibility hold. |
| FrogML | Python 3.13.18 | FrogML 3.0 still declares `>=3.9,<3.14`. The previous digest already contained 3.13.18; the explicit tag and current digest refresh its identity without claiming a Python-version upgrade. |
| Puppet | Ruby 3.3.13 | Supported runtime line; replaces 3.3.6 with the current [security patch](https://www.ruby-lang.org/en/news/2026/10/23/ruby-3-3-13-released/). |
| Hex/Mix | Elixir 1.20.5, OTP 29.1.2, Alpine 3.24.3 | Current stable Elixir patch, preserving the supported OTP/Alpine combination. |

The selected Python, Ruby, Hex and Fedora manifest indexes contain Linux amd64 and arm64 images. Their exact digests are in the [generated image inventory](ci-client-images.md) and the ledger's hashed input files.

Conan 1 and Composer 1 remain named compatibility lanes alongside their current primary clients. Puppet 8.10.0 remains the current public open-source gem; the commercial Puppet Core product does not change that documented scope. Conda protocol proof uses micromamba.

The .NET 10 digest already contains current stable SDK 10.0.403. .NET 11 remains a separate prerelease compatibility lane. Maven 3.9 and npm 12 have separate compatibility coverage; Maven 4 release candidates and Node 27 alphas are not selected as stable primary replacements. Bazel 9.3.1 matches the pinned executable's observed version. Eclipse 4.41 is the latest official release in the [vendor release data](https://download.eclipse.org/eclipse/downloads/data.json); later milestones are excluded.

## Download and execution identities

Direct Hub/Xet/Conan wheels retain published PyPI SHA-256 checks before installation. FrogML's wheel and the two Ruby gem files are checked inside their image builds. Composer, both VS Code architectures, both IntelliJ architectures, JFrog CLI and all four Terraform platform archives retain vendor-published checksums in their executable setup files:

- [Composer 2.11.1 download](https://getcomposer.org/download/2.11.1/composer.phar.sha256sum)
- [IntelliJ amd64 checksum](https://download.jetbrains.com/idea/idea-2026.2.4.tar.gz.sha256) and [arm64 checksum](https://download.jetbrains.com/idea/idea-2026.2.4-aarch64.tar.gz.sha256)
- [VS Code amd64 metadata](https://update.code.visualstudio.com/api/update/linux-x64/stable/latest) and [arm64 metadata](https://update.code.visualstudio.com/api/update/linux-arm64/stable/latest)
- [JFrog CLI storage checksum](https://releases.jfrog.io/artifactory/api/storage/jfrog-cli/v2-jf/2.131.0/jfrog-cli-linux-amd64/jf)
- [Terraform 1.17.1 checksums](https://releases.hashicorp.com/terraform/1.17.1/terraform_1.17.1_SHA256SUMS)

The final native run must retain actual client output, built image IDs, source digests and checksums alongside its scenario and coverage result. A release-feed check or a successful image build alone does not qualify protocol behavior. The dated values in this review remain historical evidence; generated inventories and recipes follow later pin changes automatically.

Native image builds print installed client versions into the retained successful build log. This includes tool versions bundled with immutable base images, SDK package versions, and the curl inside the VCS recipe's Maven image. Eclipse and IntelliJ expose their installed product version/build metadata from checksum-verified distributions; the native scenarios separately prove those distributions execute. Hex's version query runs in offline mode.
