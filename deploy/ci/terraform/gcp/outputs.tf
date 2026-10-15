output "project_number" {
  description = "The lab project's number, part of every workload identity principal."
  value       = data.google_project.this.number
}

output "workload_identity_provider" {
  description = "Provider for google-github-actions/auth, for both environments (CONFORMANCE_GCP_WIF_PROVIDER and PLATFORM_GCP_WIF_PROVIDER)."
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "conformance_service_account" {
  description = "The data-plane service account (CONFORMANCE_GCP_SERVICE_ACCOUNT); gcp/single-bucket grants it its bucket."
  value       = google_service_account.conformance.email
}

output "deployer_service_account" {
  description = "The deployer service account (PLATFORM_GCP_SERVICE_ACCOUNT)."
  value       = google_service_account.deployer.email
}

output "state_bucket" {
  description = "Remote state bucket for this bootstrap and every scenario run (PLATFORM_GCP_STATE_BUCKET)."
  value       = google_storage_bucket.state.name
}

output "dns_zone" {
  description = "The Cloud DNS managed zone scenarios add records to."
  value       = google_dns_managed_zone.gcp.name
}

output "dns_name_servers" {
  description = "Name servers for the NS records at GoDaddy (gcp.artifact under weaveplatform.run)."
  value       = google_dns_managed_zone.gcp.name_servers
}

output "project_id" {
  description = "The lab project (PLATFORM_GCP_PROJECT)."
  value       = var.project_id
}
