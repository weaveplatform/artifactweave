# One private S3 bucket a scenario stores blobs in, whose bucket policy grants
# the role artifactweave runs as. Scenarios call it once per store
# (multi-region calls it per region); it holds no provider block. Every
# resource names its region (the AWS provider's per-resource region, from
# 6.0), so one provider configuration serves buckets in several regions
# (decision 0030).

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AWS-0089: no access logging. CloudTrail records the management calls, and
#   server access logs need a second bucket.
# - AWS-0090: no versioning. Blobs are content-addressed and never rewritten,
#   and versioning would keep every deleted upload part (research 18).
# - AWS-0132 (on the encryption configuration below): S3-managed keys
#   (SSE-S3) are enough for scenario data; a KMS key would add a monthly fee
#   the tests never exercise.
#trivy:ignore:AWS-0089
#trivy:ignore:AWS-0090
resource "aws_s3_bucket" "blobs" {
  region = var.region
  bucket = var.name
  tags   = var.tags
  # Scenario data only: destroy removes the objects with the bucket.
  force_destroy = true
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

# Backstops for runs that stop before cleaning up: abandoned upload parts,
# and multipart uploads, which S3 bills until aborted.
resource "aws_s3_bucket_lifecycle_configuration" "blobs" {
  region = var.region
  bucket = aws_s3_bucket.blobs.id

  rule {
    id     = "abandoned-upload-parts"
    status = "Enabled"
    filter {
      prefix = "${var.store_prefix}_uploads/"
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
}

locals {
  bucket_arn = "arn:aws:s3:::${aws_s3_bucket.blobs.bucket}"
}

# The app role gets this bucket's objects through the bucket policy, so its
# rights end with the bucket and the scenario carries them away when
# destroyed. Within one account a bucket policy alone grants access.
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
        Principal = { AWS = var.app_role_arn }
        Action = [
          "s3:AbortMultipartUpload", "s3:DeleteObject", "s3:GetObject", "s3:ListMultipartUploadParts",
          "s3:PutObject",
        ]
        Resource = "${local.bucket_arn}/*"
      },
      {
        Sid       = "AppListing"
        Effect    = "Allow"
        Principal = { AWS = var.app_role_arn }
        Action    = ["s3:ListBucket", "s3:ListBucketMultipartUploads"]
        Resource  = local.bucket_arn
      },
    ]
  })
  depends_on = [aws_s3_bucket_public_access_block.blobs]
}
