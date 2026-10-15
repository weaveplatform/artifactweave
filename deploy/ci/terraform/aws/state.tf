# Remote state for this bootstrap (bootstrap/aws.tfstate) and every scenario
# run (scenarios/<cloud>/<scenario>/<instance>), so an apply dispatch and a
# later destroy dispatch find the same state (decision 0030). It holds
# kilobytes; S3 has no fixed fee, so it stays when scenarios are torn down.
# The name carries a fixed random suffix rather than the account ID, which
# this public repository does not record.
#
# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AWS-0089: no access logging. CloudTrail records the management calls, and
#   server access logs need a second bucket.
# - AWS-0132 (on the encryption configuration below): S3-managed keys
#   (SSE-S3). A customer-managed KMS key would add a monthly key fee and KMS
#   grants for every reader, for kilobytes of state.
#trivy:ignore:AWS-0089
resource "aws_s3_bucket" "state" {
  # Fixed: backend.tf names it, and a backend block cannot read a variable.
  bucket = "artifactweave-tfstate-56920abd"
  # State is the one thing here that must not vanish with a mistaken
  # destroy: the bucket refuses while it holds objects.
  force_destroy = false
}

resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Every apply writes a new version; old ones are the undo history.
resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

#trivy:ignore:AWS-0132
resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    id     = "prune-old-versions"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days           = 90
      newer_noncurrent_versions = 20
    }
  }

  rule {
    id     = "abort-abandoned-uploads"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  depends_on = [aws_s3_bucket_versioning.state]
}

# State travels over TLS only.
resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "TLSOnly"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [local.state_bucket_arn, "${local.state_bucket_arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
  depends_on = [aws_s3_bucket_public_access_block.state]
}
