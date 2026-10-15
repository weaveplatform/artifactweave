output "bucket" {
  description = "The bucket's name, for blob.s3.bucket."
  value       = aws_s3_bucket.blobs.bucket
}

output "arn" {
  description = "The bucket's ARN, for policies and replication rules that name it."
  value       = local.bucket_arn
}

output "region" {
  description = "The bucket's region, for blob.s3.region."
  value       = var.region
}

output "versioned" {
  description = "Whether versioning is on, which S3 replication requires of both buckets."
  value       = var.versioning
}

output "host" {
  description = "The bucket's virtual-hosted endpoint, where the SDK's presigned URLs point."
  value       = "${aws_s3_bucket.blobs.bucket}.s3.${var.region}.amazonaws.com"
}

output "signed_url_prefix" {
  description = "What a presigned URL for an object in this bucket starts with after the scheme."
  value       = "${aws_s3_bucket.blobs.bucket}.s3.${var.region}.amazonaws.com/"
}
