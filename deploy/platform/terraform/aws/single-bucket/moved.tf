# The conformance environment (make aws-apply, in the operator's local state)
# was created before this scenario composed modules/aws/storage. These keep
# its bucket: a plan moves each resource into module.storage and changes it
# in place, instead of destroying the bucket and the data under local/.

# The conformance environment's bucket.
moved {
  from = aws_s3_bucket.blobs
  to   = module.storage.aws_s3_bucket.blobs
}

# The conformance environment's ownership controls.
moved {
  from = aws_s3_bucket_ownership_controls.blobs
  to   = module.storage.aws_s3_bucket_ownership_controls.blobs
}

# The conformance environment's public access block.
moved {
  from = aws_s3_bucket_public_access_block.blobs
  to   = module.storage.aws_s3_bucket_public_access_block.blobs
}

# The conformance environment's encryption configuration.
moved {
  from = aws_s3_bucket_server_side_encryption_configuration.blobs
  to   = module.storage.aws_s3_bucket_server_side_encryption_configuration.blobs
}

# The conformance environment's lifecycle rules.
moved {
  from = aws_s3_bucket_lifecycle_configuration.blobs
  to   = module.storage.aws_s3_bucket_lifecycle_configuration.blobs
}

# The conformance environment's bucket policy.
moved {
  from = aws_s3_bucket_policy.blobs
  to   = module.storage.aws_s3_bucket_policy.blobs
}
