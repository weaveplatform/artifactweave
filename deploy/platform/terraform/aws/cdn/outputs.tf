output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: one s3 store, the bucket, with CloudFront in front of it. It authenticates through the AWS SDK chain, as the app role, and signs CloudFront URLs with the private key at cloudfront_private_key_file."
  value = yamlencode({
    blob = {
      backend = "s3"
      s3 = merge(
        { bucket = module.storage.bucket, region = var.region },
        var.store_prefix == "" ? {} : { prefix = var.store_prefix },
        {
          cdn = {
            cloudfront = {
              domain           = module.cdn.domain
              key_pair_id      = module.cdn.key_pair_ids["artifactweave"]
              private_key_file = var.cloudfront_private_key_file
            }
          }
        },
      )
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect or cdnRedirect repository's signed URLs point: the CDN host, domain_name or the distribution's *.cloudfront.net host."
  value       = module.cdn.domain
}

output "origin_host" {
  description = "The bucket's virtual-hosted endpoint, where a downloadRedirect repository's presigned URLs point when a client sends X-JFrog-Download-Redirect-To: S3."
  value       = module.storage.host
}

output "bucket" {
  description = "The bucket, for blob.s3.bucket."
  value       = module.storage.bucket
}

output "region" {
  description = "The bucket's region, for blob.s3.region."
  value       = var.region
}

output "distribution_id" {
  description = "The CloudFront distribution's ID, for the console and CLI."
  value       = module.cdn.distribution_id
}

output "distribution_domain" {
  description = "The distribution's own *.cloudfront.net host, which domain_name's alias records point at."
  value       = module.cdn.distribution_domain
}
