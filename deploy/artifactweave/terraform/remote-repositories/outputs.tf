output "repository_keys" {
  description = "Remote repository keys by package format."
  value = {
    generic   = artifactory_remote_generic_repository.this.key
    docker    = artifactory_remote_docker_repository.this.key
    oci       = artifactory_remote_oci_repository.this.key
    helmoci   = artifactory_remote_helmoci_repository.this.key
    npm       = artifactory_remote_npm_repository.this.key
    go        = artifactory_remote_go_repository.this.key
    maven     = artifactory_remote_maven_repository.this.key
    gradle    = artifactory_remote_gradle_repository.this.key
    ivy       = artifactory_remote_ivy_repository.this.key
    sbt       = artifactory_remote_sbt_repository.this.key
    pypi      = artifactory_remote_pypi_repository.this.key
    nuget     = artifactory_remote_nuget_repository.this.key
    helm      = artifactory_remote_helm_repository.this.key
    terraform = artifactory_remote_terraform_repository.this.key
  }
}
