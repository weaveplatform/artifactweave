# OIDC token exchange on artifactweave with the jfrog/platform provider: a
# GitHub Actions provider and a generic CI provider, each with an identity
# mapping. The providers are never contacted here: their keys are only fetched
# when a CI job exchanges a token.

# GitHub Actions: only tokens from github_organization are accepted.
resource "platform_oidc_configuration" "github" {
  name          = var.github_provider_name
  description   = "GitHub Actions in ${var.github_organization}"
  provider_type = "GitHub"
  issuer_url    = "https://token.actions.githubusercontent.com"
  organization  = var.github_organization
  audience      = var.audience
}

# Any other OpenID Connect issuer, such as GitLab or Jenkins.
resource "platform_oidc_configuration" "ci" {
  name          = var.ci_provider_name
  description   = "a generic CI system"
  provider_type = "generic"
  issuer_url    = var.ci_issuer_url
  audience      = var.audience
}

# Builds of main in any repository of the organisation act as ci_bot_user.
resource "platform_oidc_identity_mapping" "main_builds" {
  name          = "main-builds"
  description   = "main branch builds"
  provider_name = platform_oidc_configuration.github.name
  priority      = 1
  claims_json = jsonencode({
    repository_owner = var.github_organization
    ref              = "refs/heads/main"
  })
  token_spec = {
    username   = var.ci_bot_user
    scope      = "applied-permissions/user"
    expires_in = 600
  }
}

# Every other build gets the readers group alone: without a username the
# token has a synthetic subject, so no auto-join group widens it.
resource "platform_oidc_identity_mapping" "readers" {
  name          = "readers"
  provider_name = platform_oidc_configuration.ci.name
  priority      = 2
  claims_json = jsonencode({
    project_path = var.ci_project_path
  })
  token_spec = {
    scope      = "applied-permissions/groups:\"${var.readers_group}\""
    expires_in = 300
  }
}
