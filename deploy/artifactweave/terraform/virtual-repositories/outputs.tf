output "virtual_repository_keys" {
  description = "Virtual repository keys by package type."
  value = {
    alpine    = artifactory_virtual_alpine_repository.alpine.key
    ansible   = artifactory_virtual_ansible_repository.ansible.key
    bower     = artifactory_virtual_bower_repository.bower.key
    chef      = artifactory_virtual_chef_repository.chef.key
    cocoapods = artifactory_virtual_cocoapods_repository.cocoapods.key
    composer  = artifactory_virtual_composer_repository.composer.key
    conan     = artifactory_virtual_conan_repository.conan.key
    conda     = artifactory_virtual_conda_repository.conda.key
    cran      = artifactory_virtual_cran_repository.cran.key
    debian    = artifactory_virtual_debian_repository.debian.key
    docker    = artifactory_virtual_docker_repository.docker.key
    gems      = artifactory_virtual_gems_repository.gems.key
    generic   = artifactory_virtual_generic_repository.generic.key
    gitlfs    = artifactory_virtual_gitlfs_repository.gitlfs.key
    go        = artifactory_virtual_go_repository.go.key
    gradle    = artifactory_virtual_gradle_repository.gradle.key
    helm      = artifactory_virtual_helm_repository.helm.key
    helmoci   = artifactory_virtual_helmoci_repository.helmoci.key
    hex       = artifactory_virtual_hex_repository.hex.key
    ivy       = artifactory_virtual_ivy_repository.ivy.key
    maven     = artifactory_virtual_maven_repository.maven.key
    nix       = artifactory_virtual_nix_repository.nix.key
    npm       = artifactory_virtual_npm_repository.npm.key
    nuget     = artifactory_virtual_nuget_repository.nuget.key
    oci       = artifactory_virtual_oci_repository.oci.key
    p2        = artifactory_virtual_p2_repository.p2.key
    pub       = artifactory_virtual_pub_repository.pub.key
    puppet    = artifactory_virtual_puppet_repository.puppet.key
    pypi      = artifactory_virtual_pypi_repository.pypi.key
    rpm       = artifactory_virtual_rpm_repository.rpm.key
    sbt       = artifactory_virtual_sbt_repository.sbt.key
    swift     = artifactory_virtual_swift_repository.swift.key
    terraform = artifactory_virtual_terraform_repository.terraform.key
  }
}

output "virtual_members" {
  description = "Each virtual's member keys by package type, in the order this revision gives them; the default deployment repository is the local one."
  value       = local.ordered
}
