# Identity on artifactweave with the JFrog providers: users, groups, a
# permission over one repository, a scoped token and a signing key pair.

resource "artifactory_local_generic_repository" "team" {
  key         = var.repository_key
  description = "the repository the team permission covers"
}

# platform_group writes through Access v2; membership is left to the users'
# groups attribute, so only one resource manages it.
resource "platform_group" "developers" {
  name        = var.developers_group
  description = "developers who read and deploy to ${var.repository_key}"
}

# artifactory_group writes through the legacy security API.
resource "artifactory_group" "auditors" {
  name        = var.auditors_group
  description = "read-only auditors"
}

resource "artifactory_user" "developer" {
  name     = var.developer_user
  email    = "${var.developer_user}@${var.email_domain}"
  password = var.user_password
  groups   = [platform_group.developers.name, artifactory_group.auditors.name]
}

resource "artifactory_managed_user" "ci" {
  name     = var.ci_user
  email    = "${var.ci_user}@${var.email_domain}"
  password = var.user_password
  groups   = [platform_group.developers.name]
}

resource "platform_permission" "team" {
  name = var.permission_name

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
# through Access v2 is read back through the legacy API here: run.sh reads
# it from the state, so nothing in this configuration refers to it.
# tflint-ignore: terraform_unused_declarations
data "artifactory_permission_target" "team" {
  name = platform_permission.team.name
}

# A token for the CI user that can read the repository and nothing else, even
# though its group could deploy there too.
resource "artifactory_scoped_token" "ci" {
  username    = artifactory_managed_user.ci.name
  scopes      = ["artifact:${artifactory_local_generic_repository.team.key}:r"]
  expires_in  = 86400
  description = "read ${var.repository_key}"
}

resource "artifactory_keypair" "signing" {
  pair_name   = var.keypair_name
  pair_type   = "RSA"
  alias       = var.keypair_name
  private_key = var.keypair_private_key
  public_key  = var.keypair_public_key
}
