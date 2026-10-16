output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml for the bucket. The store authenticates through the AWS SDK chain, as the app role."
  value = yamlencode({
    blob = {
      backend = "s3"
      s3 = merge(
        { bucket = module.storage.bucket, region = var.region },
        var.store_prefix == "" ? {} : { prefix = var.store_prefix },
      )
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect repository's presigned URLs point: the bucket's virtual-hosted endpoint."
  value       = module.storage.host
}

output "bucket" {
  description = "The bucket, for blob.s3.bucket and the real-cloud conformance tests (ARTIFACTWEAVE_TEST_S3_BUCKET)."
  value       = module.storage.bucket
}

output "region" {
  description = "The bucket's region, for blob.s3.region and the real-cloud conformance tests (ARTIFACTWEAVE_TEST_S3_REGION)."
  value       = var.region
}
