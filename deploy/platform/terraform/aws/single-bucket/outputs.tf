output "bucket" {
  description = "Bucket for blob.s3.bucket and ARTIFACTWEAVE_TEST_S3_BUCKET (CONFORMANCE_S3_BUCKET)."
  value       = aws_s3_bucket.blobs.bucket
}

output "region" {
  description = "Region for blob.s3.region and ARTIFACTWEAVE_TEST_S3_REGION."
  value       = var.region
}

output "store_prefix" {
  description = "Key prefix the local run writes under (blob.s3.prefix)."
  value       = var.store_prefix
}

output "app_role_arn" {
  description = "The role the bucket policy grants: the bootstrap's conformance role, unless app_role_arn names another."
  value       = local.app_role_arn
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml for this bucket; credentials come from the AWS default chain."
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

output "storage_host" {
  description = "Host a downloadRedirect repository's signed URLs point at: the bucket's virtual-hosted endpoint, which the SDK presigns."
  value       = "${aws_s3_bucket.blobs.bucket}.s3.${var.region}.amazonaws.com"
}

output "expires_at" {
  description = "The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
