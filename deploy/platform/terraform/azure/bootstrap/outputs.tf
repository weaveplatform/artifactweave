output "subscription_id" {
  description = "The lab subscription, for azure/login (CONFORMANCE_AZURE_SUBSCRIPTION_ID and PLATFORM_AZURE_SUBSCRIPTION_ID)."
  value       = var.subscription_id
}

output "tenant_id" {
  description = "The tenant, for azure/login (CONFORMANCE_AZURE_TENANT_ID and PLATFORM_AZURE_TENANT_ID)."
  value       = data.azurerm_client_config.current.tenant_id
}

output "conformance_client_id" {
  description = "The data-plane identity's client ID (CONFORMANCE_AZURE_CLIENT_ID); azure/single-bucket grants it its account."
  value       = azurerm_user_assigned_identity.conformance.client_id
}

output "conformance_identity" {
  description = "The data-plane identity's name, which azure/single-bucket looks up."
  value       = azurerm_user_assigned_identity.conformance.name
}

output "deployer_client_id" {
  description = "The deployer identity's client ID (PLATFORM_AZURE_CLIENT_ID)."
  value       = azurerm_user_assigned_identity.deployer.client_id
}

output "resource_group" {
  description = "The bootstrap's resource group, holding the identities, state account and DNS zone."
  value       = azurerm_resource_group.bootstrap.name
}

output "scenarios_resource_group" {
  description = "The resource group scenarios are created in (PLATFORM_AZURE_RESOURCE_GROUP)."
  value       = azurerm_resource_group.scenarios.name
}

output "state_storage_account" {
  description = "Remote state account for this bootstrap and every scenario run (PLATFORM_AZURE_STATE_ACCOUNT)."
  value       = azurerm_storage_account.state.name
}

output "state_container" {
  description = "Container inside the state account."
  value       = azurerm_storage_container.state.name
}

output "dns_name_servers" {
  description = "Name servers for the NS records at GoDaddy (azure.artifact under weaveplatform.run)."
  value       = azurerm_dns_zone.azure.name_servers
}
