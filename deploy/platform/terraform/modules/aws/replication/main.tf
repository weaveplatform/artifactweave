# S3 replication from one bucket to another, and the IAM role S3 assumes to
# do it. aws/geo-replicated calls it with two modules/aws/storage buckets in
# different regions; it holds no provider block, and the replication rule
# names the source bucket's region (the AWS provider's per-resource region),
# so one provider configuration serves both (decision 0030).

data "aws_caller_identity" "current" {}

locals {
  # Only S3 may assume the role, and only for this source bucket in this
  # account: a bucket name is global and can be recreated by another account
  # after a delete, so the ARN alone would not pin it.
  trust = {
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "s3.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = {
        StringEquals = { "aws:SourceAccount" = data.aws_caller_identity.current.account_id }
        ArnLike      = { "aws:SourceArn" = var.source_bucket.arn }
      }
    }]
  }
}

resource "aws_iam_role" "replication" {
  name                 = var.name
  path                 = var.iam_role_path
  description          = "S3 replication from ${var.source_bucket.bucket} to ${var.destination_bucket.bucket}."
  permissions_boundary = var.permissions_boundary_arn
  assume_role_policy   = jsonencode(local.trust)
  tags                 = var.tags
}

# Read the source's configuration and versions, write the destination's
# copies, and nothing else. Deleting a copy is granted only when deletes
# replicate.
resource "aws_iam_role_policy" "replication" {
  name = "replicate"
  role = aws_iam_role.replication.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ReadSourceConfiguration"
        Effect   = "Allow"
        Action   = ["s3:GetReplicationConfiguration", "s3:ListBucket"]
        Resource = var.source_bucket.arn
      },
      {
        Sid    = "ReadSourceVersions"
        Effect = "Allow"
        Action = [
          "s3:GetObjectVersionAcl", "s3:GetObjectVersionForReplication", "s3:GetObjectVersionTagging",
        ]
        Resource = "${var.source_bucket.arn}/*"
      },
      {
        Sid      = "WriteDestination"
        Effect   = "Allow"
        Action   = concat(["s3:ReplicateObject", "s3:ReplicateTags"], var.replicate_deletes ? ["s3:ReplicateDelete"] : [])
        Resource = "${var.destination_bucket.arn}/*"
      },
    ]
  })
}

# Replication Time Control is opt-in because it is billed per GB replicated;
# its only valid threshold is 15 minutes, and it needs the metrics.
resource "aws_s3_bucket_replication_configuration" "replication" {
  region = var.source_bucket.region
  bucket = var.source_bucket.bucket
  role   = aws_iam_role.replication.arn

  # S3 refuses the rule until the role can read the source; the policy is a
  # separate resource, so the role alone would not order it.
  depends_on = [aws_iam_role_policy.replication]

  rule {
    id     = "replicate"
    status = "Enabled"

    filter {
      prefix = var.prefix
    }

    delete_marker_replication {
      status = var.replicate_deletes ? "Enabled" : "Disabled"
    }

    destination {
      bucket        = var.destination_bucket.arn
      storage_class = var.storage_class

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
