# Virtual repositories on artifactweave with the jfrog/artifactory provider:
# one artifactory_virtual_<type>_repository for every type provider 12.11.14
# has one, each over a local and a remote member and deploying to the local
# one. Every virtual field the provider offers is set away from its default,
# so the empty plan after apply shows artifactweave stores and returns each
# one as written. This reference manages configuration only: no request
# resolves through these virtuals, and the remotes fetch nothing.

# Signing key pairs the virtuals name. Debian and RPM sign their merged
# metadata with GPG pairs, and the Maven family's key_pair is a GPG pair too;
# Alpine and Hex sign with RSA, which artifactweave checks when a repository
# names the pair. The provider cannot validate a GPG private key, so plan
# warns "Usage of GPG can't be validated"; the server checks that the two
# halves match.
resource "artifactory_keypair" "gpg_primary" {
  pair_name   = var.gpg_primary_keypair_name
  pair_type   = "GPG"
  alias       = var.gpg_primary_keypair_name
  private_key = var.gpg_primary_private_key
  public_key  = var.gpg_primary_public_key
}

resource "artifactory_keypair" "gpg_secondary" {
  pair_name   = var.gpg_secondary_keypair_name
  pair_type   = "GPG"
  alias       = var.gpg_secondary_keypair_name
  private_key = var.gpg_secondary_private_key
  public_key  = var.gpg_secondary_public_key
}

resource "artifactory_keypair" "rsa" {
  pair_name   = var.rsa_keypair_name
  pair_type   = "RSA"
  alias       = var.rsa_keypair_name
  private_key = var.rsa_private_key
  public_key  = var.rsa_public_key
}

# Revision 2 reverses every virtual's members and changes one field of each:
# the retrieval cache period, the external dependency patterns, the POM
# cleanup policy, or else the notes. Each virtual also takes a layout other
# than the provider's default for its type: simple-default where that is a
# type's own layout, and maven-2-default where it is simple-default.
locals {
  ordered = { for type, members in local.members : type => var.revision == 1 ? members : reverse(members) }

  includes_pattern               = "com/example/**,org/example/**"
  excludes_pattern               = "**/*-SNAPSHOT/**"
  retrieval_cache_period_seconds = var.revision == 1 ? 650 : 900
  external_dependencies_patterns = var.revision == 1 ? ["**/github.com/**"] : ["**/github.com/**", "**/gitlab.com/**"]
  pom_cleanup_policy             = var.revision == 1 ? "discard_any_reference" : "nothing"
}

# Each virtual's members in revision 1's order, local first; the local one is
# its default deployment repository in both revisions.
locals {
  members = {
    docker    = [artifactory_local_docker_v2_repository.docker.key, artifactory_remote_docker_repository.docker.key]
    oci       = [artifactory_local_oci_repository.oci.key, artifactory_remote_oci_repository.oci.key]
    helmoci   = [artifactory_local_helmoci_repository.helmoci.key, artifactory_remote_helmoci_repository.helmoci.key]
    npm       = [artifactory_local_npm_repository.npm.key, artifactory_remote_npm_repository.npm.key]
    go        = [artifactory_local_go_repository.go.key, artifactory_remote_go_repository.go.key]
    maven     = [artifactory_local_maven_repository.maven.key, artifactory_remote_maven_repository.maven.key]
    gradle    = [artifactory_local_gradle_repository.gradle.key, artifactory_remote_gradle_repository.gradle.key]
    ivy       = [artifactory_local_ivy_repository.ivy.key, artifactory_remote_ivy_repository.ivy.key]
    sbt       = [artifactory_local_sbt_repository.sbt.key, artifactory_remote_sbt_repository.sbt.key]
    pypi      = [artifactory_local_pypi_repository.pypi.key, artifactory_remote_pypi_repository.pypi.key]
    nuget     = [artifactory_local_nuget_repository.nuget.key, artifactory_remote_nuget_repository.nuget.key]
    helm      = [artifactory_local_helm_repository.helm.key, artifactory_remote_helm_repository.helm.key]
    terraform = [artifactory_local_terraform_module_repository.terraform.key, artifactory_remote_terraform_repository.terraform.key]
    generic   = [artifactory_local_generic_repository.generic.key, artifactory_remote_generic_repository.generic.key]
    alpine    = [artifactory_local_alpine_repository.alpine.key, artifactory_remote_alpine_repository.alpine.key]
    bower     = [artifactory_local_bower_repository.bower.key, artifactory_remote_bower_repository.bower.key]
    conan     = [artifactory_local_conan_repository.conan.key, artifactory_remote_conan_repository.conan.key]
    debian    = [artifactory_local_debian_repository.debian.key, artifactory_remote_debian_repository.debian.key]
    rpm       = [artifactory_local_rpm_repository.rpm.key, artifactory_remote_rpm_repository.rpm.key]
    cocoapods = [artifactory_local_cocoapods_repository.cocoapods.key, artifactory_remote_cocoapods_repository.cocoapods.key]
    composer  = [artifactory_local_composer_repository.composer.key, artifactory_remote_composer_repository.composer.key]
    gems      = [artifactory_local_gems_repository.gems.key, artifactory_remote_gems_repository.gems.key]
    gitlfs    = [artifactory_local_gitlfs_repository.gitlfs.key, artifactory_remote_gitlfs_repository.gitlfs.key]
    p2        = [artifactory_local_maven_repository.p2.key, artifactory_remote_p2_repository.p2.key]
    pub       = [artifactory_local_pub_repository.pub.key, artifactory_remote_pub_repository.pub.key]
    puppet    = [artifactory_local_puppet_repository.puppet.key, artifactory_remote_puppet_repository.puppet.key]
    swift     = [artifactory_local_swift_repository.swift.key, artifactory_remote_swift_repository.swift.key]
    ansible   = [artifactory_local_ansible_repository.ansible.key, artifactory_remote_ansible_repository.ansible.key]
    chef      = [artifactory_local_chef_repository.chef.key, artifactory_remote_chef_repository.chef.key]
    conda     = [artifactory_local_conda_repository.conda.key, artifactory_remote_conda_repository.conda.key]
    cran      = [artifactory_local_cran_repository.cran.key, artifactory_remote_cran_repository.cran.key]
    hex       = [artifactory_local_hex_repository.hex.key, artifactory_remote_hex_repository.hex.key]
    nix       = [artifactory_local_nix_repository.nix.key, artifactory_remote_nix_repository.nix.key]
  }
}

# The eight families and generic.

resource "artifactory_local_docker_v2_repository" "docker" {
  key         = "${var.repository_prefix}-docker-local"
  description = "local member of the docker virtual"
}

resource "artifactory_remote_docker_repository" "docker" {
  key         = "${var.repository_prefix}-docker-remote"
  url         = "${var.upstream_url}/docker"
  description = "remote member of the docker virtual"
}

resource "artifactory_virtual_docker_repository" "docker" {
  key                                                = "${var.repository_prefix}-docker"
  repositories                                       = local.ordered["docker"]
  default_deployment_repo                            = artifactory_local_docker_v2_repository.docker.key
  description                                        = "docker virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  resolve_docker_tags_by_timestamp                   = true
}

resource "artifactory_local_oci_repository" "oci" {
  key         = "${var.repository_prefix}-oci-local"
  description = "local member of the oci virtual"
}

resource "artifactory_remote_oci_repository" "oci" {
  key         = "${var.repository_prefix}-oci-remote"
  url         = "${var.upstream_url}/oci"
  description = "remote member of the oci virtual"
}

resource "artifactory_virtual_oci_repository" "oci" {
  key                                                = "${var.repository_prefix}-oci"
  repositories                                       = local.ordered["oci"]
  default_deployment_repo                            = artifactory_local_oci_repository.oci.key
  description                                        = "oci virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  resolve_oci_tags_by_timestamp                      = true
}

resource "artifactory_local_helmoci_repository" "helmoci" {
  key         = "${var.repository_prefix}-helmoci-local"
  description = "local member of the helmoci virtual"
}

resource "artifactory_remote_helmoci_repository" "helmoci" {
  key         = "${var.repository_prefix}-helmoci-remote"
  url         = "${var.upstream_url}/helmoci"
  description = "remote member of the helmoci virtual"
}

resource "artifactory_virtual_helmoci_repository" "helmoci" {
  key                                                = "${var.repository_prefix}-helmoci"
  repositories                                       = local.ordered["helmoci"]
  default_deployment_repo                            = artifactory_local_helmoci_repository.helmoci.key
  description                                        = "helmoci virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  resolve_oci_tags_by_timestamp                      = true
}

resource "artifactory_local_npm_repository" "npm" {
  key         = "${var.repository_prefix}-npm-local"
  description = "local member of the npm virtual"
}

resource "artifactory_remote_npm_repository" "npm" {
  key         = "${var.repository_prefix}-npm-remote"
  url         = "${var.upstream_url}/npm"
  description = "remote member of the npm virtual"
}

resource "artifactory_virtual_npm_repository" "npm" {
  key                                                = "${var.repository_prefix}-npm"
  repositories                                       = local.ordered["npm"]
  default_deployment_repo                            = artifactory_local_npm_repository.npm.key
  description                                        = "npm virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
  external_dependencies_enabled                      = true
  external_dependencies_patterns                     = local.external_dependencies_patterns
  external_dependencies_remote_repo                  = artifactory_remote_npm_repository.npm.key
}

resource "artifactory_local_go_repository" "go" {
  key         = "${var.repository_prefix}-go-local"
  description = "local member of the go virtual"
}

resource "artifactory_remote_go_repository" "go" {
  key         = "${var.repository_prefix}-go-remote"
  url         = "${var.upstream_url}/go"
  description = "remote member of the go virtual"
}

resource "artifactory_virtual_go_repository" "go" {
  key                                                = "${var.repository_prefix}-go"
  repositories                                       = local.ordered["go"]
  default_deployment_repo                            = artifactory_local_go_repository.go.key
  description                                        = "go virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  # On by default for Go, so off here.
  external_dependencies_enabled  = false
  external_dependencies_patterns = local.external_dependencies_patterns
}

resource "artifactory_local_maven_repository" "maven" {
  key         = "${var.repository_prefix}-maven-local"
  description = "local member of the maven virtual"
}

resource "artifactory_remote_maven_repository" "maven" {
  key         = "${var.repository_prefix}-maven-remote"
  url         = "${var.upstream_url}/maven"
  description = "remote member of the maven virtual"
}

resource "artifactory_virtual_maven_repository" "maven" {
  key                                                = "${var.repository_prefix}-maven"
  repositories                                       = local.ordered["maven"]
  default_deployment_repo                            = artifactory_local_maven_repository.maven.key
  description                                        = "maven virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  force_maven_authentication                         = true
  pom_repository_references_cleanup_policy           = local.pom_cleanup_policy
  key_pair                                           = artifactory_keypair.gpg_primary.pair_name
}

resource "artifactory_local_gradle_repository" "gradle" {
  key         = "${var.repository_prefix}-gradle-local"
  description = "local member of the gradle virtual"
}

resource "artifactory_remote_gradle_repository" "gradle" {
  key         = "${var.repository_prefix}-gradle-remote"
  url         = "${var.upstream_url}/gradle"
  description = "remote member of the gradle virtual"
}

resource "artifactory_virtual_gradle_repository" "gradle" {
  key                                                = "${var.repository_prefix}-gradle"
  repositories                                       = local.ordered["gradle"]
  default_deployment_repo                            = artifactory_local_gradle_repository.gradle.key
  description                                        = "gradle virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  force_maven_authentication                         = true
  pom_repository_references_cleanup_policy           = local.pom_cleanup_policy
  key_pair                                           = artifactory_keypair.gpg_primary.pair_name
}

resource "artifactory_local_ivy_repository" "ivy" {
  key         = "${var.repository_prefix}-ivy-local"
  description = "local member of the ivy virtual"
}

resource "artifactory_remote_ivy_repository" "ivy" {
  key         = "${var.repository_prefix}-ivy-remote"
  url         = "${var.upstream_url}/ivy"
  description = "remote member of the ivy virtual"
}

resource "artifactory_virtual_ivy_repository" "ivy" {
  key                                                = "${var.repository_prefix}-ivy"
  repositories                                       = local.ordered["ivy"]
  default_deployment_repo                            = artifactory_local_ivy_repository.ivy.key
  description                                        = "ivy virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  force_maven_authentication                         = true
  pom_repository_references_cleanup_policy           = local.pom_cleanup_policy
  key_pair                                           = artifactory_keypair.gpg_primary.pair_name
}

resource "artifactory_local_sbt_repository" "sbt" {
  key         = "${var.repository_prefix}-sbt-local"
  description = "local member of the sbt virtual"
}

resource "artifactory_remote_sbt_repository" "sbt" {
  key         = "${var.repository_prefix}-sbt-remote"
  url         = "${var.upstream_url}/sbt"
  description = "remote member of the sbt virtual"
}

resource "artifactory_virtual_sbt_repository" "sbt" {
  key                                                = "${var.repository_prefix}-sbt"
  repositories                                       = local.ordered["sbt"]
  default_deployment_repo                            = artifactory_local_sbt_repository.sbt.key
  description                                        = "sbt virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  force_maven_authentication                         = true
  pom_repository_references_cleanup_policy           = local.pom_cleanup_policy
  key_pair                                           = artifactory_keypair.gpg_primary.pair_name
}

resource "artifactory_local_pypi_repository" "pypi" {
  key         = "${var.repository_prefix}-pypi-local"
  description = "local member of the pypi virtual"
}

resource "artifactory_remote_pypi_repository" "pypi" {
  key               = "${var.repository_prefix}-pypi-remote"
  url               = "${var.upstream_url}/pypi"
  description       = "remote member of the pypi virtual"
  pypi_registry_url = "${var.upstream_url}/pypi/registry"
}

resource "artifactory_virtual_pypi_repository" "pypi" {
  key                                                = "${var.repository_prefix}-pypi"
  repositories                                       = local.ordered["pypi"]
  default_deployment_repo                            = artifactory_local_pypi_repository.pypi.key
  description                                        = "pypi virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_nuget_repository" "nuget" {
  key         = "${var.repository_prefix}-nuget-local"
  description = "local member of the nuget virtual"
}

resource "artifactory_remote_nuget_repository" "nuget" {
  key         = "${var.repository_prefix}-nuget-remote"
  url         = "${var.upstream_url}/nuget"
  description = "remote member of the nuget virtual"
  v3_feed_url = "${var.upstream_url}/nuget/v3/index.json"
}

resource "artifactory_virtual_nuget_repository" "nuget" {
  key                                                = "${var.repository_prefix}-nuget"
  repositories                                       = local.ordered["nuget"]
  default_deployment_repo                            = artifactory_local_nuget_repository.nuget.key
  description                                        = "nuget virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  force_nuget_authentication                         = true
}

resource "artifactory_local_helm_repository" "helm" {
  key         = "${var.repository_prefix}-helm-local"
  description = "local member of the helm virtual"
}

resource "artifactory_remote_helm_repository" "helm" {
  key                  = "${var.repository_prefix}-helm-remote"
  url                  = "${var.upstream_url}/helm"
  description          = "remote member of the helm virtual"
  helm_charts_base_url = "${var.upstream_url}/helm/charts"
}

resource "artifactory_virtual_helm_repository" "helm" {
  key                                                = "${var.repository_prefix}-helm"
  repositories                                       = local.ordered["helm"]
  default_deployment_repo                            = artifactory_local_helm_repository.helm.key
  description                                        = "helm virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
  use_namespaces                                     = true
}

resource "artifactory_local_terraform_module_repository" "terraform" {
  key         = "${var.repository_prefix}-terraform-local"
  description = "local member of the terraform virtual"
}

# The registry and providers URLs point at the upstream too, so no remote
# names a public host. The provider's default registry URL is the public
# Terraform registry, for which 7.161.26 turns bypass_head_requests on at
# every save (PackageRepoConfigChangeInterceptor), so the plan would no
# longer match this configuration.
resource "artifactory_remote_terraform_repository" "terraform" {
  key                     = "${var.repository_prefix}-terraform-remote"
  url                     = "${var.upstream_url}/terraform"
  description             = "remote member of the terraform virtual"
  terraform_registry_url  = "${var.upstream_url}/terraform/modules"
  terraform_providers_url = "${var.upstream_url}/terraform/providers"
}

resource "artifactory_virtual_terraform_repository" "terraform" {
  key                                                = "${var.repository_prefix}-terraform"
  repositories                                       = local.ordered["terraform"]
  default_deployment_repo                            = artifactory_local_terraform_module_repository.terraform.key
  description                                        = "terraform virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_generic_repository" "generic" {
  key         = "${var.repository_prefix}-generic-local"
  description = "local member of the generic virtual"
}

resource "artifactory_remote_generic_repository" "generic" {
  key         = "${var.repository_prefix}-generic-remote"
  url         = "${var.upstream_url}/generic"
  description = "remote member of the generic virtual"
}

resource "artifactory_virtual_generic_repository" "generic" {
  key                                                = "${var.repository_prefix}-generic"
  repositories                                       = local.ordered["generic"]
  default_deployment_repo                            = artifactory_local_generic_repository.generic.key
  description                                        = "generic virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

# Every other type the provider has a virtual resource for.

resource "artifactory_local_alpine_repository" "alpine" {
  key         = "${var.repository_prefix}-alpine-local"
  description = "local member of the alpine virtual"
}

resource "artifactory_remote_alpine_repository" "alpine" {
  key         = "${var.repository_prefix}-alpine-remote"
  url         = "${var.upstream_url}/alpine"
  description = "remote member of the alpine virtual"
}

resource "artifactory_virtual_alpine_repository" "alpine" {
  key                                                = "${var.repository_prefix}-alpine"
  repositories                                       = local.ordered["alpine"]
  default_deployment_repo                            = artifactory_local_alpine_repository.alpine.key
  description                                        = "alpine virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
  primary_keypair_ref                                = artifactory_keypair.rsa.pair_name
}

resource "artifactory_local_bower_repository" "bower" {
  key         = "${var.repository_prefix}-bower-local"
  description = "local member of the bower virtual"
}

resource "artifactory_remote_bower_repository" "bower" {
  key                = "${var.repository_prefix}-bower-remote"
  url                = "${var.upstream_url}/bower"
  description        = "remote member of the bower virtual"
  bower_registry_url = "${var.upstream_url}/bower/registry"
}

resource "artifactory_virtual_bower_repository" "bower" {
  key                                                = "${var.repository_prefix}-bower"
  repositories                                       = local.ordered["bower"]
  default_deployment_repo                            = artifactory_local_bower_repository.bower.key
  description                                        = "bower virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  external_dependencies_enabled                      = true
  external_dependencies_patterns                     = local.external_dependencies_patterns
  external_dependencies_remote_repo                  = artifactory_remote_bower_repository.bower.key
}

resource "artifactory_local_conan_repository" "conan" {
  key         = "${var.repository_prefix}-conan-local"
  description = "local member of the conan virtual"
}

resource "artifactory_remote_conan_repository" "conan" {
  key         = "${var.repository_prefix}-conan-remote"
  url         = "${var.upstream_url}/conan"
  description = "remote member of the conan virtual"
}

resource "artifactory_virtual_conan_repository" "conan" {
  key                                                = "${var.repository_prefix}-conan"
  repositories                                       = local.ordered["conan"]
  default_deployment_repo                            = artifactory_local_conan_repository.conan.key
  description                                        = "conan virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
  force_conan_authentication                         = true
}

resource "artifactory_local_debian_repository" "debian" {
  key         = "${var.repository_prefix}-debian-local"
  description = "local member of the debian virtual"
}

resource "artifactory_remote_debian_repository" "debian" {
  key         = "${var.repository_prefix}-debian-remote"
  url         = "${var.upstream_url}/debian"
  description = "remote member of the debian virtual"
}

resource "artifactory_virtual_debian_repository" "debian" {
  key                                                = "${var.repository_prefix}-debian"
  repositories                                       = local.ordered["debian"]
  default_deployment_repo                            = artifactory_local_debian_repository.debian.key
  description                                        = "debian virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
  debian_default_architectures                       = "amd64,arm64"
  optional_index_compression_formats                 = ["bz2", "xz"]
  primary_keypair_ref                                = artifactory_keypair.gpg_primary.pair_name
  secondary_keypair_ref                              = artifactory_keypair.gpg_secondary.pair_name
}

resource "artifactory_local_rpm_repository" "rpm" {
  key         = "${var.repository_prefix}-rpm-local"
  description = "local member of the rpm virtual"
}

resource "artifactory_remote_rpm_repository" "rpm" {
  key         = "${var.repository_prefix}-rpm-remote"
  url         = "${var.upstream_url}/rpm"
  description = "remote member of the rpm virtual"
}

resource "artifactory_virtual_rpm_repository" "rpm" {
  key                                                = "${var.repository_prefix}-rpm"
  repositories                                       = local.ordered["rpm"]
  default_deployment_repo                            = artifactory_local_rpm_repository.rpm.key
  description                                        = "rpm virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
  primary_keypair_ref                                = artifactory_keypair.gpg_primary.pair_name
  secondary_keypair_ref                              = artifactory_keypair.gpg_secondary.pair_name
}

resource "artifactory_local_cocoapods_repository" "cocoapods" {
  key         = "${var.repository_prefix}-cocoapods-local"
  description = "local member of the cocoapods virtual"
}

resource "artifactory_remote_cocoapods_repository" "cocoapods" {
  key                 = "${var.repository_prefix}-cocoapods-remote"
  url                 = "${var.upstream_url}/cocoapods"
  description         = "remote member of the cocoapods virtual"
  pods_specs_repo_url = "${var.upstream_url}/cocoapods/specs"
}

resource "artifactory_virtual_cocoapods_repository" "cocoapods" {
  key                                                = "${var.repository_prefix}-cocoapods"
  repositories                                       = local.ordered["cocoapods"]
  default_deployment_repo                            = artifactory_local_cocoapods_repository.cocoapods.key
  description                                        = "cocoapods virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_composer_repository" "composer" {
  key         = "${var.repository_prefix}-composer-local"
  description = "local member of the composer virtual"
}

resource "artifactory_remote_composer_repository" "composer" {
  key                   = "${var.repository_prefix}-composer-remote"
  url                   = "${var.upstream_url}/composer"
  description           = "remote member of the composer virtual"
  composer_registry_url = "${var.upstream_url}/composer/packagist"
}

resource "artifactory_virtual_composer_repository" "composer" {
  key                                                = "${var.repository_prefix}-composer"
  repositories                                       = local.ordered["composer"]
  default_deployment_repo                            = artifactory_local_composer_repository.composer.key
  description                                        = "composer virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_gems_repository" "gems" {
  key         = "${var.repository_prefix}-gems-local"
  description = "local member of the gems virtual"
}

resource "artifactory_remote_gems_repository" "gems" {
  key         = "${var.repository_prefix}-gems-remote"
  url         = "${var.upstream_url}/gems"
  description = "remote member of the gems virtual"
}

resource "artifactory_virtual_gems_repository" "gems" {
  key                                                = "${var.repository_prefix}-gems"
  repositories                                       = local.ordered["gems"]
  default_deployment_repo                            = artifactory_local_gems_repository.gems.key
  description                                        = "gems virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_gitlfs_repository" "gitlfs" {
  key         = "${var.repository_prefix}-gitlfs-local"
  description = "local member of the gitlfs virtual"
}

resource "artifactory_remote_gitlfs_repository" "gitlfs" {
  key         = "${var.repository_prefix}-gitlfs-remote"
  url         = "${var.upstream_url}/gitlfs"
  description = "remote member of the gitlfs virtual"
}

resource "artifactory_virtual_gitlfs_repository" "gitlfs" {
  key                                                = "${var.repository_prefix}-gitlfs"
  repositories                                       = local.ordered["gitlfs"]
  default_deployment_repo                            = artifactory_local_gitlfs_repository.gitlfs.key
  description                                        = "gitlfs virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

# There is no local P2 repository, so the P2 virtual's local member is a
# Maven repository holding an update site. 7.161.26 lets a P2 virtual
# aggregate only the Maven group (Maven, Gradle, Ivy, SBT and P2) and
# refuses a Generic member (VirtualRepoConfigChangeInterceptor; decision
# 0008, R11).
resource "artifactory_local_maven_repository" "p2" {
  key         = "${var.repository_prefix}-p2-local"
  description = "local member of the p2 virtual"
}

resource "artifactory_remote_p2_repository" "p2" {
  key         = "${var.repository_prefix}-p2-remote"
  url         = "${var.upstream_url}/p2"
  description = "remote member of the p2 virtual"
}

resource "artifactory_virtual_p2_repository" "p2" {
  key                                                = "${var.repository_prefix}-p2"
  repositories                                       = local.ordered["p2"]
  default_deployment_repo                            = artifactory_local_maven_repository.p2.key
  description                                        = "p2 virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_pub_repository" "pub" {
  key         = "${var.repository_prefix}-pub-local"
  description = "local member of the pub virtual"
}

resource "artifactory_remote_pub_repository" "pub" {
  key         = "${var.repository_prefix}-pub-remote"
  url         = "${var.upstream_url}/pub"
  description = "remote member of the pub virtual"
}

resource "artifactory_virtual_pub_repository" "pub" {
  key                                                = "${var.repository_prefix}-pub"
  repositories                                       = local.ordered["pub"]
  default_deployment_repo                            = artifactory_local_pub_repository.pub.key
  description                                        = "pub virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_puppet_repository" "puppet" {
  key         = "${var.repository_prefix}-puppet-local"
  description = "local member of the puppet virtual"
}

resource "artifactory_remote_puppet_repository" "puppet" {
  key         = "${var.repository_prefix}-puppet-remote"
  url         = "${var.upstream_url}/puppet"
  description = "remote member of the puppet virtual"
}

resource "artifactory_virtual_puppet_repository" "puppet" {
  key                                                = "${var.repository_prefix}-puppet"
  repositories                                       = local.ordered["puppet"]
  default_deployment_repo                            = artifactory_local_puppet_repository.puppet.key
  description                                        = "puppet virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_swift_repository" "swift" {
  key         = "${var.repository_prefix}-swift-local"
  description = "local member of the swift virtual"
}

resource "artifactory_remote_swift_repository" "swift" {
  key         = "${var.repository_prefix}-swift-remote"
  url         = "${var.upstream_url}/swift"
  description = "remote member of the swift virtual"
}

resource "artifactory_virtual_swift_repository" "swift" {
  key                                                = "${var.repository_prefix}-swift"
  repositories                                       = local.ordered["swift"]
  default_deployment_repo                            = artifactory_local_swift_repository.swift.key
  description                                        = "swift virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}

resource "artifactory_local_ansible_repository" "ansible" {
  key         = "${var.repository_prefix}-ansible-local"
  description = "local member of the ansible virtual"
}

resource "artifactory_remote_ansible_repository" "ansible" {
  key         = "${var.repository_prefix}-ansible-remote"
  url         = "${var.upstream_url}/ansible"
  description = "remote member of the ansible virtual"
}

resource "artifactory_virtual_ansible_repository" "ansible" {
  key                                                = "${var.repository_prefix}-ansible"
  repositories                                       = local.ordered["ansible"]
  default_deployment_repo                            = artifactory_local_ansible_repository.ansible.key
  description                                        = "ansible virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
}

resource "artifactory_local_chef_repository" "chef" {
  key         = "${var.repository_prefix}-chef-local"
  description = "local member of the chef virtual"
}

resource "artifactory_remote_chef_repository" "chef" {
  key         = "${var.repository_prefix}-chef-remote"
  url         = "${var.upstream_url}/chef"
  description = "remote member of the chef virtual"
}

resource "artifactory_virtual_chef_repository" "chef" {
  key                                                = "${var.repository_prefix}-chef"
  repositories                                       = local.ordered["chef"]
  default_deployment_repo                            = artifactory_local_chef_repository.chef.key
  description                                        = "chef virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
}

resource "artifactory_local_conda_repository" "conda" {
  key         = "${var.repository_prefix}-conda-local"
  description = "local member of the conda virtual"
}

resource "artifactory_remote_conda_repository" "conda" {
  key         = "${var.repository_prefix}-conda-remote"
  url         = "${var.upstream_url}/conda"
  description = "remote member of the conda virtual"
}

resource "artifactory_virtual_conda_repository" "conda" {
  key                                                = "${var.repository_prefix}-conda"
  repositories                                       = local.ordered["conda"]
  default_deployment_repo                            = artifactory_local_conda_repository.conda.key
  description                                        = "conda virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
}

resource "artifactory_local_cran_repository" "cran" {
  key         = "${var.repository_prefix}-cran-local"
  description = "local member of the cran virtual"
}

resource "artifactory_remote_cran_repository" "cran" {
  key         = "${var.repository_prefix}-cran-remote"
  url         = "${var.upstream_url}/cran"
  description = "remote member of the cran virtual"
}

resource "artifactory_virtual_cran_repository" "cran" {
  key                                                = "${var.repository_prefix}-cran"
  repositories                                       = local.ordered["cran"]
  default_deployment_repo                            = artifactory_local_cran_repository.cran.key
  description                                        = "cran virtual over a local and a remote member"
  notes                                              = "managed by Terraform"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  retrieval_cache_period_seconds                     = local.retrieval_cache_period_seconds
}

resource "artifactory_local_hex_repository" "hex" {
  key                     = "${var.repository_prefix}-hex-local"
  description             = "local member of the hex virtual"
  hex_primary_keypair_ref = artifactory_keypair.rsa.pair_name
}

resource "artifactory_remote_hex_repository" "hex" {
  key                     = "${var.repository_prefix}-hex-remote"
  url                     = "${var.upstream_url}/hex"
  description             = "remote member of the hex virtual"
  hex_primary_keypair_ref = artifactory_keypair.rsa.pair_name
  public_key              = var.rsa_public_key
}

resource "artifactory_virtual_hex_repository" "hex" {
  key                                                = "${var.repository_prefix}-hex"
  repositories                                       = local.ordered["hex"]
  default_deployment_repo                            = artifactory_local_hex_repository.hex.key
  description                                        = "hex virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "maven-2-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
  hex_primary_keypair_ref                            = artifactory_keypair.rsa.pair_name
}

resource "artifactory_local_nix_repository" "nix" {
  key         = "${var.repository_prefix}-nix-local"
  description = "local member of the nix virtual"
}

resource "artifactory_remote_nix_repository" "nix" {
  key         = "${var.repository_prefix}-nix-remote"
  url         = "${var.upstream_url}/nix"
  description = "remote member of the nix virtual"
}

resource "artifactory_virtual_nix_repository" "nix" {
  key                                                = "${var.repository_prefix}-nix"
  repositories                                       = local.ordered["nix"]
  default_deployment_repo                            = artifactory_local_nix_repository.nix.key
  description                                        = "nix virtual over a local and a remote member"
  notes                                              = "managed by Terraform, revision ${var.revision}"
  includes_pattern                                   = local.includes_pattern
  excludes_pattern                                   = local.excludes_pattern
  repo_layout_ref                                    = "simple-default"
  artifactory_requests_can_retrieve_remote_artifacts = true
  hide_unauthorized_resources                        = true
}
