output "buckets" {
  description = "The member buckets, by region."
  value       = { for r, m in module.member : r => m.bucket }
}

output "service_account" {
  description = "The service account artifactweave runs as (the bootstrap's conformance account unless app_service_account names another), which signs download URLs."
  value       = local.app_service_account
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: a mirror of the two buckets, preferring the primary region."
  value = yamlencode({
    blob = {
      backend = "mirror"
      stores = {
        for r in local.regions : local.members[r].store => {
          backend = "gcs"
          zone    = r
          gcs = {
            bucket           = module.member[r].bucket
            prefix           = var.store_prefix
            google_access_id = local.app_service_account
          }
        }
      }
      mirror = {
        members      = [for r in local.regions : local.members[r].store]
        zone         = var.primary_region
        write_quorum = var.write_quorum
      }
    }
  })
}

output "storage_host" {
  description = "Host a downloadRedirect repository's signed URLs point at, whichever member signs them."
  value       = "storage.googleapis.com"
}

output "mirror_members" {
  description = "The mirror's members in order (this instance's zone first): store name, zone, and what a signed URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it."
  value = [
    for r in local.regions : {
      store             = local.members[r].store
      zone              = r
      signed_url_prefix = module.member[r].signed_url_prefix
    }
  ]
}

output "expires_at" {
  description = "The expires-at label (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
