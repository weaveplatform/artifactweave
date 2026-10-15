output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: one store, the primary bucket. It authenticates through the AWS SDK chain, as the app role."
  value = yamlencode({
    blob = {
      backend = "s3"
      s3 = merge(
        { bucket = module.primary.bucket, region = var.region },
        var.store_prefix == "" ? {} : { prefix = var.store_prefix },
      )
    }
  })
}

output "failover_artifactweave_config" {
  description = "The same blob section pointed at the replica, for a failover: re-point artifactweave here and restart it."
  value = yamlencode({
    blob = {
      backend = "s3"
      s3 = merge(
        { bucket = module.replica.bucket, region = var.replica_region },
        var.store_prefix == "" ? {} : { prefix = var.store_prefix },
      )
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect repository's presigned URLs point: the primary bucket's virtual-hosted endpoint."
  value       = module.primary.host
}

output "bucket" {
  description = "The primary bucket, for blob.s3.bucket."
  value       = module.primary.bucket
}

output "region" {
  description = "The primary bucket's region, for blob.s3.region."
  value       = var.region
}

output "replica_bucket" {
  description = "The replica bucket S3 copies the committed blobs into."
  value       = module.replica.bucket
}

output "replica_region" {
  description = "The replica bucket's region."
  value       = var.replica_region
}

output "replicated_prefix" {
  description = "Keys under this prefix, the committed blobs, replicate."
  value       = module.replication.prefix
}
