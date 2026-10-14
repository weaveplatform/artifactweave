# OIDC token exchange on artifactweave with the jfrog/platform provider: a
# GitHub Actions provider and a generic CI provider, each with an identity
# mapping. test/terraform/run.sh applies it in CI, requires an empty plan,
# then destroys it. The providers are never contacted: their keys are only
# fetched when a CI job exchanges a token.

terraform {
  required_version = ">= 1.6"
  required_providers {
    platform = {
      source = "jfrog/platform"
      # Keep in step with the pins and checksums in test/terraform/run.sh.
      version = "2.2.11"
    }
  }
}

variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory."
}

variable "access_token" {
  type        = string
  description = "Admin access token, sent as a Bearer token."
  sensitive   = true
}

provider "platform" {
  url          = var.url
  access_token = var.access_token
}

# GitHub Actions: only tokens from the octo-org organisation are accepted.
resource "platform_oidc_configuration" "github" {
  name          = "tf-github"
  description   = "GitHub Actions in octo-org"
  provider_type = "GitHub"
  issuer_url    = "https://token.actions.githubusercontent.com"
  organization  = "octo-org"
  audience      = "artifactweave"
}

# Any other OpenID Connect issuer, such as GitLab or Jenkins.
resource "platform_oidc_configuration" "ci" {
  name          = "tf-ci"
  description   = "a generic CI system"
  provider_type = "generic"
  issuer_url    = "https://ci.example.com"
  audience      = "artifactweave"
}

# Builds of main in any octo-org repository act as the ci-bot user.
resource "platform_oidc_identity_mapping" "main_builds" {
  name          = "main-builds"
  description   = "main branch builds"
  provider_name = platform_oidc_configuration.github.name
  priority      = 1
  claims_json = jsonencode({
    repository_owner = "octo-org"
    ref              = "refs/heads/main"
  })
  token_spec = {
    username   = "ci-bot"
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
    project_path = "octo-org/**"
  })
  token_spec = {
    scope      = "applied-permissions/groups:\"readers\""
    expires_in = 300
  }
}
