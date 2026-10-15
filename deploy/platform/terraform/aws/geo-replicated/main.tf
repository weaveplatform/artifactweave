# A primary S3 bucket that S3 cross-region replication copies into a replica
# bucket in a second region, for operators who want the cloud's own
# replication rather than artifactweave's mirror. artifactweave writes to and
# signs URLs for the primary only; the replica is S3's copy, invisible to
# artifactweave, and the failover target (the failover_artifactweave_config
# output). Apply aws/bootstrap first: it holds GitHub's trust, the roles and
# the permissions boundary every role here must carry.

data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

# The role artifactweave runs as: the bootstrap's conformance role locally,
# the deployer in a scenario run (which runs artifactweave in the same job and
# cannot read the bootstrap's roles, so the lookup is skipped).
data "aws_iam_role" "conformance" {
  count = var.app_role_arn == "" ? 1 : 0
  name  = var.conformance_role_name
}

resource "random_id" "suffix" {
  byte_length = 4
}

locals {
  app_role_arn = var.app_role_arn != "" ? var.app_role_arn : data.aws_iam_role.conformance[0].arn
  partition    = data.aws_partition.current.partition
  account_id   = data.aws_caller_identity.current.account_id

  # aws/bootstrap's boundary. The deployer may create roles only under the
  # scenario path and only with this boundary attached.
  boundary_arn = "arn:${local.partition}:iam::${local.account_id}:policy/artifactweave/${var.boundary_policy_name}"

  # Only committed blobs replicate; temporary upload parts and conformance
  # prefixes stay in the primary, where the lifecycle rules remove them.
  replicated_prefix = "${var.store_prefix}sha256/"

  buckets = {
    primary = aws_s3_bucket.blobs
    replica = aws_s3_bucket.replica
  }
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AWS-0089: no access logging. CloudTrail records the management calls, and
#   server access logs need another bucket in each region.
# - AWS-0132 (on the encryption configurations below): S3-managed keys
#   (SSE-S3); a KMS key in each region would add a monthly fee and a
#   replication key grant the scenario never exercises.
#trivy:ignore:AWS-0089
resource "aws_s3_bucket" "blobs" {
  bucket = "${var.name}-${random_id.suffix.hex}"
  # Scenario runs (platform-scenarios.yml) tag the bucket with the scenario,
  # the instance and expires-at, which reap matches.
  tags = var.scenario_labels
  # Reference data only: destroy removes every object version with the
  # bucket.
  force_destroy = true
}

#trivy:ignore:AWS-0089
resource "aws_s3_bucket" "replica" {
  provider      = aws.replica
  bucket        = "${var.name}-${random_id.suffix.hex}-r"
  tags          = var.scenario_labels
  force_destroy = true
}

# Replication needs versioning on both buckets (research 20). Blobs are never
# rewritten, so versions accumulate only from deletes; the lifecycle rules
# expire them.
resource "aws_s3_bucket_versioning" "blobs" {
  bucket = aws_s3_bucket.blobs.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_versioning" "replica" {
  provider = aws.replica
  bucket   = aws_s3_bucket.replica.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_ownership_controls" "blobs" {
  bucket = aws_s3_bucket.blobs.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_ownership_controls" "replica" {
  provider = aws.replica
  bucket   = aws_s3_bucket.replica.id
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

resource "aws_s3_bucket_public_access_block" "replica" {
  provider                = aws.replica
  bucket                  = aws_s3_bucket.replica.id
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

#trivy:ignore:AWS-0132
resource "aws_s3_bucket_server_side_encryption_configuration" "replica" {
  provider = aws.replica
  bucket   = aws_s3_bucket.replica.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Backstops for runs that stop before cleaning up, and the versions deletes
# leave behind. The replica gets the same rules: a failover writes there.
resource "aws_s3_bucket_lifecycle_configuration" "blobs" {
  bucket     = aws_s3_bucket.blobs.id
  depends_on = [aws_s3_bucket_versioning.blobs]

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
    id     = "deleted-versions"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
    noncurrent_version_expiration {
      noncurrent_days = 1
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "replica" {
  provider   = aws.replica
  bucket     = aws_s3_bucket.replica.id
  depends_on = [aws_s3_bucket_versioning.replica]

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
    id     = "deleted-versions"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
    noncurrent_version_expiration {
      noncurrent_days = 1
    }
  }
}

# The app role gets the objects of both buckets through their bucket
# policies: the primary in normal running, the replica after a failover.
# Within one account a bucket policy alone grants access.
resource "aws_s3_bucket_policy" "blobs" {
  bucket     = aws_s3_bucket.blobs.id
  policy     = data.aws_iam_policy_document.bucket["primary"].json
  depends_on = [aws_s3_bucket_public_access_block.blobs]
}

resource "aws_s3_bucket_policy" "replica" {
  provider   = aws.replica
  bucket     = aws_s3_bucket.replica.id
  policy     = data.aws_iam_policy_document.bucket["replica"].json
  depends_on = [aws_s3_bucket_public_access_block.replica]
}

data "aws_iam_policy_document" "bucket" {
  for_each = local.buckets

  statement {
    sid     = "TLSOnly"
    effect  = "Deny"
    actions = ["s3:*"]
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    resources = [each.value.arn, "${each.value.arn}/*"]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  statement {
    sid = "AppObjects"
    actions = [
      "s3:AbortMultipartUpload", "s3:DeleteObject", "s3:GetObject", "s3:ListMultipartUploadParts",
      "s3:PutObject",
    ]
    principals {
      type        = "AWS"
      identifiers = [local.app_role_arn]
    }
    resources = ["${each.value.arn}/*"]
  }

  statement {
    sid     = "AppListing"
    actions = ["s3:ListBucket", "s3:ListBucketMultipartUploads"]
    principals {
      type        = "AWS"
      identifiers = [local.app_role_arn]
    }
    resources = [each.value.arn]
  }
}

# The role S3 assumes to replicate. It lives under the scenario path and
# carries the bootstrap's boundary, the only role the deployer may create;
# its trust names this source bucket and account, so no other bucket can
# use it.
resource "aws_iam_role" "replication" {
  name                 = "${substr(var.name, 0, 40)}-${random_id.suffix.hex}-repl"
  path                 = var.scenario_role_path
  description          = "S3 cross-region replication for ${aws_s3_bucket.blobs.bucket}."
  permissions_boundary = local.boundary_arn
  assume_role_policy   = data.aws_iam_policy_document.replication_trust.json
  tags                 = var.scenario_labels
}

data "aws_iam_policy_document" "replication_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["s3.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = [aws_s3_bucket.blobs.arn]
    }
  }
}

resource "aws_iam_role_policy" "replication" {
  name   = "replicate"
  role   = aws_iam_role.replication.id
  policy = data.aws_iam_policy_document.replication.json
}

data "aws_iam_policy_document" "replication" {
  statement {
    sid       = "ReadSourceConfiguration"
    actions   = ["s3:GetReplicationConfiguration", "s3:ListBucket"]
    resources = [aws_s3_bucket.blobs.arn]
  }

  statement {
    sid = "ReadSourceVersions"
    actions = [
      "s3:GetObjectVersionForReplication", "s3:GetObjectVersionAcl", "s3:GetObjectVersionTagging",
    ]
    resources = ["${aws_s3_bucket.blobs.arn}/*"]
  }

  statement {
    sid       = "WriteReplica"
    actions   = ["s3:ReplicateObject", "s3:ReplicateDelete", "s3:ReplicateTags"]
    resources = ["${aws_s3_bucket.replica.arn}/*"]
  }
}

# Committed blobs only, deletes not replicated: a blob artifactweave's GC
# removes from the primary stays in the replica until the replica's own
# lifecycle or an operator removes it, so a failover never misses a blob a
# lagging delete would have taken. Replication Time Control (15 minutes for
# 99.9% of objects) is opt-in because it is billed per GB replicated.
resource "aws_s3_bucket_replication_configuration" "blobs" {
  bucket = aws_s3_bucket.blobs.id
  role   = aws_iam_role.replication.arn

  depends_on = [
    aws_s3_bucket_versioning.blobs,
    aws_s3_bucket_versioning.replica,
    aws_iam_role_policy.replication,
  ]

  rule {
    id     = "committed-blobs"
    status = "Enabled"

    filter {
      prefix = local.replicated_prefix
    }

    delete_marker_replication {
      status = "Disabled"
    }

    destination {
      bucket        = aws_s3_bucket.replica.arn
      storage_class = "STANDARD"

      dynamic "replication_time" {
        for_each = var.replication_time_control ? [1] : []
        content {
          status = "Enabled"
          time {
            minutes = 15
          }
        }
      }

      dynamic "metrics" {
        for_each = var.replication_time_control ? [1] : []
        content {
          status = "Enabled"
          event_threshold {
            minutes = 15
          }
        }
      }
    }
  }
}
