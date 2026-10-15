output "providers" {
  description = "Names of the GitHub Actions and generic CI OIDC providers, as CI jobs pass them to the token exchange."
  value = {
    github = platform_oidc_configuration.github.name
    ci     = platform_oidc_configuration.ci.name
  }
}

output "identity_mappings" {
  description = "Names of the identity mappings, by provider."
  value = {
    github = platform_oidc_identity_mapping.main_builds.name
    ci     = platform_oidc_identity_mapping.readers.name
  }
}
