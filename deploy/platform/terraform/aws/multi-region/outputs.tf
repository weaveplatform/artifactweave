output "buckets" {
  description = "The member buckets, by region."
  value       = { for r, m in module.member : r => m.bucket }
}

output "app_role_arn" {
  description = "The role both bucket policies grant: the bootstrap's conformance role, unless app_role_arn names another."
  value       = local.app_role_arn
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: a mirror of the two buckets, preferring the primary region. Credentials come from the AWS default chain."
  value = yamlencode({
    blob = {
      backend = "mirror"
      stores = {
        for r in local.regions : local.members[r].store => {
          backend = "s3"
          zone    = r
          s3 = {
            bucket = module.member[r].bucket
            region = r
            prefix = var.store_prefix
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
  description = "Host a downloadRedirect repository's signed URLs point at when the primary member signs them, as it does once it holds the blob."
  value       = module.member[var.primary_region].host
}

output "mirror_members" {
  description = "The mirror's members in order (this instance's zone first): store name, zone, and what a presigned URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it."
  value = [
    for r in local.regions : {
      store             = local.members[r].store
      zone              = r
      signed_url_prefix = module.member[r].signed_url_prefix
    }
  ]
}

output "expires_at" {
  description = "The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
