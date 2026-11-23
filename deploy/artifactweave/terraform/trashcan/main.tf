# A singleton: Artifactory always has a trash can policy, so destroy resets
# it to the documented default (on, 14 days) rather than removing it.
resource "artifactory_trashcan_config" "this" {
  enabled               = var.enabled
  retention_period_days = var.retention_period_days
}
