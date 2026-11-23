resource "artifactory_general_security" "this" {
  enable_anonymous_access = var.enable_anonymous_access
  encryption_policy       = var.encryption_policy
}
