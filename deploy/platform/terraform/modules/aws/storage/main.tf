# One private S3 bucket and its bucket policy, granting the roles in
# var.principals its objects. Scenarios call it once per store; it holds no
# provider block. Every resource names its region (the AWS provider's
# per-resource region, from 6.0), so one provider configuration serves
# buckets in several regions (decision 0030).

locals {
  # As the s3 backend normalises blob.s3.prefix, so the lifecycle rules match
  # the keys it writes.
  trimmed      = trimprefix(var.store_prefix, "/")
  store_prefix = local.trimmed == "" || endswith(local.trimmed, "/") ? local.trimmed : "${local.trimmed}/"
  bucket_arn   = "arn:aws:s3:::${aws_s3_bucket.blobs.bucket}"
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AWS-0089: no access logging. CloudTrail records the management calls, and
#   server access logs need a second bucket.
# - AWS-0090: versioning is off unless var.versioning turns it on. Blobs are
#   content-addressed and never rewritten, and versioning would keep every
#   deleted upload part (research 18); replication is what needs it.
# - AWS-0132 (on the encryption configuration below): S3-managed keys
#   (SSE-S3); a KMS key would add a monthly fee and a key policy to manage.
#trivy:ignore:AWS-0089
#trivy:ignore:AWS-0090
resource "aws_s3_bucket" "blobs" {
  region        = var.region
  bucket        = var.name
  force_destroy = var.force_destroy
  tags          = var.tags
}

resource "aws_s3_bucket_ownership_controls" "blobs" {
  region = var.region
  bucket = aws_s3_bucket.blobs.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "blobs" {
  region                  = var.region
  bucket                  = aws_s3_bucket.blobs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

#trivy:ignore:AWS-0132
resource "aws_s3_bucket_server_side_encryption_configuration" "blobs" {
  region = var.region
  bucket = aws_s3_bucket.blobs.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_versioning" "blobs" {
  count  = var.versioning ? 1 : 0
  region = var.region
  bucket = aws_s3_bucket.blobs.id
  versioning_configuration {
    status = "Enabled"
  }
}

# Backstops for runs that stop before cleaning up: abandoned upload parts,
# multipart uploads (S3 bills their parts until aborted), the caller's
# expiring prefixes and, on a versioned bucket, the versions a delete leaves
# behind. The rules keep this order; the tests address them by position.
resource "aws_s3_bucket_lifecycle_configuration" "blobs" {
  region = var.region
  bucket = aws_s3_bucket.blobs.id

  rule {
    id     = "abandoned-upload-parts"
    status = "Enabled"
    filter {
      prefix = "${local.store_prefix}_uploads/"
    }
    expiration {
      days = 2
    }
  }

  rule {
    id     = "abort-incomplete-multipart-uploads"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  dynamic "rule" {
    for_each = var.versioning ? [1] : []
    content {
      id     = "expire-noncurrent-versions"
      status = "Enabled"
      filter {}
      noncurrent_version_expiration {
        noncurrent_days = 1
      }
      expiration {
        expired_object_delete_marker = true
      }
    }
  }

  dynamic "rule" {
    for_each = var.expiring_prefixes
    content {
      id     = "expire-${rule.key}"
      status = "Enabled"
      filter {
        prefix = rule.key
      }
      expiration {
        days = rule.value
      }
    }
  }

  depends_on = [aws_s3_bucket_versioning.blobs]
}

# The principals get this bucket's objects through the bucket policy, so
# their rights end with the bucket and leave with it. Within one account a
# bucket policy alone grants access.
resource "aws_s3_bucket_policy" "blobs" {
  region = var.region
  bucket = aws_s3_bucket.blobs.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "TLSOnly"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:*"
        Resource  = [local.bucket_arn, "${local.bucket_arn}/*"]
        Condition = { Bool = { "aws:SecureTransport" = "false" } }
      },
      {
        Sid       = "AppObjects"
        Effect    = "Allow"
        Principal = { AWS = values(var.principals) }
        Action = [
          "s3:AbortMultipartUpload", "s3:DeleteObject", "s3:GetObject", "s3:ListMultipartUploadParts",
          "s3:PutObject",
        ]
        Resource = "${local.bucket_arn}/*"
      },
      {
        Sid       = "AppListing"
        Effect    = "Allow"
        Principal = { AWS = values(var.principals) }
        Action    = ["s3:ListBucket", "s3:ListBucketMultipartUploads"]
        Resource  = local.bucket_arn
      },
    ]
  })
  depends_on = [aws_s3_bucket_public_access_block.blobs]
}
