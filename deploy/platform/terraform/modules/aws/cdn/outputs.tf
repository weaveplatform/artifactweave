output "domain" {
  description = "The host artifactweave signs URLs for (blob.s3.cdn.cloudfront.domain): domain_name when given, otherwise the distribution's *.cloudfront.net host."
  value       = local.custom ? var.domain_name : aws_cloudfront_distribution.cdn.domain_name
  # A host of your own answers only once its alias records exist.
  depends_on = [aws_route53_record.alias]
}

output "distribution_domain" {
  description = "The distribution's own *.cloudfront.net host, which domain_name's alias records point at."
  value       = aws_cloudfront_distribution.cdn.domain_name
}

output "distribution_id" {
  description = "The distribution's ID, for the CloudFront console and CLI."
  value       = aws_cloudfront_distribution.cdn.id
}

output "distribution_arn" {
  description = "The distribution's ARN, which the bucket policy statement names as the only source CloudFront may read for."
  value       = aws_cloudfront_distribution.cdn.arn
}

output "key_pair_ids" {
  description = "Each public key's ID by its name in public_keys: blob.s3.cdn.cloudfront.key_pair_id for the key whose private half artifactweave signs with."
  value       = { for name, k in aws_cloudfront_public_key.signers : name => k.id }
}

output "bucket_policy_statement" {
  description = "The bucket policy statement that lets this distribution read the committed blobs: pass it to modules/aws/storage's policy_statements."
  value       = local.statement
}
