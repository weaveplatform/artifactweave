# Identity on artifactweave with the JFrog providers: users, groups, a
# permission over one repository, a scoped token and a signing key pair.
# test/terraform/run.sh applies it in CI, requires an empty plan, then
# destroys it.

terraform {
  required_providers {
    artifactory = {
      source = "jfrog/artifactory"
      # Keep in step with the pins and checksums in test/terraform/run.sh.
      version = "12.11.14"
    }
    platform = {
      source  = "jfrog/platform"
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
  description = "Admin access token; both providers send it as a Bearer token."
  sensitive   = true
}

variable "user_password" {
  type        = string
  description = "Password for the developer and CI users. It must satisfy the server's password policy."
  sensitive   = true
}

variable "keypair_private_key" {
  type        = string
  description = "PEM RSA private key for the signing key pair."
  sensitive   = true
}

variable "keypair_public_key" {
  type        = string
  description = "PEM public key matching keypair_private_key."
}

provider "artifactory" {
  url          = var.url
  access_token = var.access_token
}

provider "platform" {
  url          = var.url
  access_token = var.access_token
}

resource "artifactory_local_generic_repository" "team" {
  key         = "tf-team-local"
  description = "the repository the team permission covers"
}

# platform_group writes through Access v2; membership is left to the users'
# groups attribute, so only one resource manages it.
resource "platform_group" "developers" {
  name        = "tf-developers"
  description = "developers who read and deploy to tf-team-local"
}

# artifactory_group writes through the legacy security API.
resource "artifactory_group" "auditors" {
  name        = "tf-auditors"
  description = "read-only auditors"
}

resource "artifactory_user" "developer" {
  name     = "tf-developer"
  email    = "tf-developer@example.com"
  password = var.user_password
  groups   = [platform_group.developers.name, artifactory_group.auditors.name]
}

resource "artifactory_managed_user" "ci" {
  name     = "tf-ci"
  email    = "tf-ci@example.com"
  password = var.user_password
  groups   = [platform_group.developers.name]
}

resource "platform_permission" "team" {
  name = "tf-team"

  artifact = {
    actions = {
      users = [
        {
          name        = artifactory_user.developer.name
          permissions = ["READ", "WRITE", "ANNOTATE"]
        },
      ]
      groups = [
        {
          name        = platform_group.developers.name
          permissions = ["READ", "WRITE"]
        },
        {
          name        = artifactory_group.auditors.name
          permissions = ["READ"]
        },
      ]
    }

    targets = [
      {
        name             = artifactory_local_generic_repository.team.key
        include_patterns = ["**"]
      },
    ]
  }
}

# artifactory_permission_target is a stub in provider 12.x: every operation
# returns "deprecated. Use platform_permission instead". Its data source
# still reads the v2 permission target API, so the permission written
# through Access v2 is read back through the legacy API here.
data "artifactory_permission_target" "team" {
  name = platform_permission.team.name
}

# A token for the CI user that can read tf-team-local and nothing else, even
# though its group could deploy there too.
resource "artifactory_scoped_token" "ci" {
  username    = artifactory_managed_user.ci.name
  scopes      = ["artifact:${artifactory_local_generic_repository.team.key}:r"]
  expires_in  = 86400
  description = "read tf-team-local"
}

resource "artifactory_keypair" "signing" {
  pair_name   = "tf-signing"
  pair_type   = "RSA"
  alias       = "tf-signing"
  private_key = var.keypair_private_key
  public_key  = var.keypair_public_key
}
