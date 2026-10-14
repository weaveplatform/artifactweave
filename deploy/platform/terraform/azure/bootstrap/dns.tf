# azure.artifact.weaveplatform.run, delegated from GoDaddy (which serves
# weaveplatform.run) by NS records naming this zone's name servers. Created
# by CLI on 2026-10-14 and imported here (imports.tf). Recreating the zone
# could assign different name servers and silently break the delegation, so
# Terraform refuses to destroy it.
resource "azurerm_dns_zone" "azure" {
  name                = "azure.artifact.weaveplatform.run"
  resource_group_name = azurerm_resource_group.bootstrap.name
  tags = {
    managed-by = "artifactweave-bootstrap"
    issue      = "126"
  }

  lifecycle {
    prevent_destroy = true
  }
}
