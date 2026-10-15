output "bucket" {
  description = "Primary bucket for blob.s3.bucket."
  value       = aws_s3_bucket.blobs.bucket
}

output "region" {
  description = "Region of the primary bucket (blob.s3.region)."
  value       = var.region
}

output "replica_bucket" {
  description = "Replica bucket S3 replicates committed blobs into."
  value       = aws_s3_bucket.replica.bucket
}

output "replica_region" {
  description = "Region of the replica bucket."
  value       = var.replica_region
}

output "store_prefix" {
  description = "Key prefix artifactweave writes under (blob.s3.prefix)."
  value       = var.store_prefix
}

output "replicated_prefix" {
  description = "Keys under this prefix (the committed blobs) replicate."
  value       = local.replicated_prefix
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: one store, the primary bucket. Credentials come from the AWS default chain."
  value = yamlencode({
    blob = {
      backend = "s3"
      s3 = {
        bucket = aws_s3_bucket.blobs.bucket
        region = var.region
        prefix = var.store_prefix
      }
    }
  })
}

output "failover_artifactweave_config" {
  description = "The same blob section pointed at the replica, for a failover: re-point artifactweave here and restart it."
  value = yamlencode({
    blob = {
      backend = "s3"
      s3 = {
        bucket = aws_s3_bucket.replica.bucket
        region = var.replica_region
        prefix = var.store_prefix
      }
    }
  })
}

output "storage_host" {
  description = "Host a downloadRedirect repository's signed URLs point at: the primary bucket's virtual-hosted endpoint."
  value       = "${aws_s3_bucket.blobs.bucket}.s3.${var.region}.amazonaws.com"
}

output "expires_at" {
  description = "The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
