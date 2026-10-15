output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: a mirror of the buckets, preferring the first region. Each store authenticates through the AWS SDK chain, as the app role."
  value = yamlencode({
    blob = {
      backend = "mirror"
      stores = {
        for r in var.regions : local.members[r].store => {
          backend = "s3"
          zone    = r
          s3 = merge(
            { bucket = module.member[r].bucket, region = r },
            var.store_prefix == "" ? {} : { prefix = var.store_prefix },
          )
        }
      }
      mirror = {
        members      = [for r in var.regions : local.members[r].store]
        zone         = var.regions[0]
        write_quorum = var.write_quorum
      }
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect repository's presigned URLs point: the first member's endpoint, which signs once it holds the blob."
  value       = module.member[var.regions[0]].host
}

output "buckets" {
  description = "The member buckets, by region."
  value       = { for r, m in module.member : r => m.bucket }
}
