output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: a mirror of the buckets, preferring the first location. Each store authenticates with Application Default Credentials, as the app service account, which also signs download URLs."
  value = yamlencode({
    blob = {
      backend = "mirror"
      stores = {
        for l in var.locations : local.members[l].store => {
          backend = "gcs"
          zone    = l
          gcs = merge(
            { bucket = module.member[l].bucket, google_access_id = var.app_service_account },
            var.store_prefix == "" ? {} : { prefix = var.store_prefix },
          )
        }
      }
      mirror = {
        members      = [for l in var.locations : local.members[l].store]
        zone         = var.locations[0]
        write_quorum = var.write_quorum
      }
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect repository's signed URLs point: Cloud Storage's path-style endpoint, the same for every member."
  value       = "storage.googleapis.com"
}

output "buckets" {
  description = "The member buckets, by location."
  value       = { for l, m in module.member : l => m.bucket }
}
