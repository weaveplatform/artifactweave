output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: a mirror of the S3 bucket, the Cloud Storage bucket and the storage account, preferring the home cloud's. Each store authenticates through its cloud's own credential chain, as that cloud's app identity."
  value = yamlencode({
    blob = {
      backend = "mirror"
      stores  = { for c in local.order : local.members[c].store => local.members[c].config }
      mirror = {
        members      = [for c in local.order : local.members[c].store]
        zone         = local.members[var.home_cloud].config.zone
        write_quorum = var.write_quorum
      }
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect repository's signed URLs point first: the home cloud's member, which signs once it holds the blob."
  value       = local.members[var.home_cloud].host
}

output "aws_bucket" {
  description = "The S3 bucket."
  value       = module.aws.bucket
}

output "gcp_bucket" {
  description = "The Cloud Storage bucket."
  value       = module.gcp.bucket
}

output "azure_account" {
  description = "The storage account."
  value       = module.azure.account
}

output "resource_group" {
  description = "The resource group holding the storage account."
  value       = local.resource_group_name
}
