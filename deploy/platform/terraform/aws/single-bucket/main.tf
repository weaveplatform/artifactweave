# An acceptance bucket for artifactweave's S3 backend: the local container
# (compose.yaml) and the real-cloud conformance tests (cloud-conformance.yml)
# both use it, under separate prefixes. GitHub's OIDC provider and the
# conformance role live in aws/bootstrap, which outlives this scenario; apply
# the bootstrap first.

# The role artifactweave runs as. Locally and for cloud-conformance.yml that
# is the data-plane role aws/bootstrap created. A scenario run names the
# deployer instead: it applies this root and runs artifactweave in the same
# job, and the boundary keeps it from reading the bootstrap's roles, so the
# lookup is skipped when the ARN is given.
data "aws_iam_role" "conformance" {
  count = var.app_role_arn == "" ? 1 : 0
  name  = var.conformance_role_name
}

locals {
  app_role_arn = var.app_role_arn != "" ? var.app_role_arn : data.aws_iam_role.conformance[0].arn
}

resource "random_id" "suffix" {
  byte_length = 4
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AWS-0089: no access logging. CloudTrail records the management calls, and
#   server access logs need a second bucket.
# - AWS-0090: no versioning. Blobs are content-addressed and never rewritten,
#   and versioning would keep every deleted upload part (research 18).
# - AWS-0132 (on the encryption configuration below): S3-managed keys
#   (SSE-S3) are enough for acceptance data; a KMS key would add a monthly fee
#   the tests never exercise.
#trivy:ignore:AWS-0089
#trivy:ignore:AWS-0090
resource "aws_s3_bucket" "blobs" {
  bucket = "${var.name}-${random_id.suffix.hex}"
  # Scenario runs (platform-scenarios.yml) tag the bucket with the scenario,
  # the instance and expires-at, which reap matches.
  tags = var.scenario_labels
  # Acceptance data only: destroy removes the objects with the bucket.
  force_destroy = true
}

resource "aws_s3_bucket_ownership_controls" "blobs" {
  bucket = aws_s3_bucket.blobs.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "blobs" {
  bucket                  = aws_s3_bucket.blobs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

#trivy:ignore:AWS-0132
resource "aws_s3_bucket_server_side_encryption_configuration" "blobs" {
  bucket = aws_s3_bucket.blobs.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Backstops for runs that stop before cleaning up: abandoned upload parts,
# conformance prefixes, and multipart uploads, which S3 bills until aborted.
resource "aws_s3_bucket_lifecycle_configuration" "blobs" {
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
    id     = "conformance-runs"
    status = "Enabled"
    filter {
      prefix = "conformance/"
    }
    expiration {
      days = 1
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

# The app role gets this bucket's objects through the bucket policy,
# so its rights end with the bucket and the scenario carries them away when
# destroyed. Within one account a bucket policy alone grants access.
resource "aws_s3_bucket_policy" "blobs" {
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
        Principal = { AWS = local.app_role_arn }
        Action = [
          "s3:AbortMultipartUpload", "s3:DeleteObject", "s3:GetObject", "s3:ListMultipartUploadParts",
          "s3:PutObject",
        ]
        Resource = "${local.bucket_arn}/*"
      },
      {
        Sid       = "AppListing"
        Effect    = "Allow"
        Principal = { AWS = local.app_role_arn }
        Action    = ["s3:ListBucket", "s3:ListBucketMultipartUploads"]
        Resource  = local.bucket_arn
      },
    ]
  })
  depends_on = [aws_s3_bucket_public_access_block.blobs]
}
