# The long-lived foundation every AWS scenario stands on (decision 0030):
# GitHub's OIDC provider and its trust in two roles, the permissions boundary
# that caps the deployer and every role a scenario creates, the remote state
# bucket, the DNS zone and a budget. An operator applies it once; scenarios
# come and go without touching it.

data "aws_caller_identity" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id

  github_issuer       = "token.actions.githubusercontent.com"
  conformance_subject = "${var.github_subject_prefix}:environment:${var.conformance_environment}"
  platform_subject    = "${var.github_subject_prefix}:environment:${var.platform_environment}"

  # Written out rather than read from the policy resource, because the
  # boundary's own conditions name it.
  boundary_name = "artifactweave-scenario-boundary"
  boundary_arn  = "arn:aws:iam::${local.account_id}:policy/artifactweave/${local.boundary_name}"

  zone_arn          = "arn:aws:route53:::hostedzone/${var.dns_zone_id}"
  scenario_role_arn = "arn:aws:iam::${local.account_id}:role${var.scenario_role_path}*"
  state_bucket_arn  = "arn:aws:s3:::${aws_s3_bucket.state.bucket}"

  # The role actions a scenario needs for its own roles (S3 replication's,
  # for one): create, configure and delete, but never without the boundary.
  scenario_role_actions = [
    "iam:AttachRolePolicy",
    "iam:CreateRole",
    "iam:DeleteRole",
    "iam:DeleteRolePolicy",
    "iam:DetachRolePolicy",
    "iam:GetRole",
    "iam:GetRolePolicy",
    "iam:ListAttachedRolePolicies",
    "iam:ListInstanceProfilesForRole",
    "iam:ListRolePolicies",
    "iam:PutRolePermissionsBoundary",
    "iam:PutRolePolicy",
    "iam:TagRole",
    "iam:UntagRole",
    "iam:UpdateAssumeRolePolicy",
    "iam:UpdateRole",
  ]
}

# GitHub Actions exchange their OIDC token here (sts:AssumeRoleWithWebIdentity),
# so no workflow stores an AWS key. AWS validates GitHub's certificate itself,
# so the provider carries no thumbprint.
resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://${local.github_issuer}"
  client_id_list = ["sts.amazonaws.com"]
}

# The data-plane role. It has no policy of its own: aws/single-bucket's bucket
# policy grants it that bucket's objects, so its rights end with the bucket.
resource "aws_iam_role" "conformance" {
  name                 = "artifactweave-conformance"
  path                 = "/artifactweave/"
  description          = "cloud-conformance.yml; objects in the single-bucket scenario's bucket only, granted by its bucket policy."
  max_session_duration = 3600
  assume_role_policy   = jsonencode(local.trust["conformance"])
}

# The deployer: creates and destroys scenario resources, behind the
# cloud-platform environment's approval. The boundary caps it, and every role
# it creates must carry the same boundary, so it can never hand out more than
# it holds.
resource "aws_iam_role" "deployer" {
  name                 = "artifactweave-deployer"
  path                 = "/artifactweave/"
  description          = "Scenario applies and destroys from the cloud-platform GitHub environment."
  max_session_duration = 3600
  assume_role_policy   = jsonencode(local.trust["deployer"])
  permissions_boundary = aws_iam_policy.scenario_boundary.arn
}

locals {
  # One trust per role, each matching one environment subject exactly:
  # StringEquals, never StringLike, and GitHub's immutable subject carries the
  # owner and repository IDs, so a renamed or recreated repository of the
  # same name cannot match.
  trust = {
    for role, subject in {
      conformance = local.conformance_subject
      deployer    = local.platform_subject
    } :
    role => {
      Version = "2012-10-17"
      Statement = [{
        Sid       = "GitHubEnvironment"
        Effect    = "Allow"
        Principal = { Federated = aws_iam_openid_connect_provider.github.arn }
        Action    = "sts:AssumeRoleWithWebIdentity"
        Condition = {
          StringEquals = {
            "${local.github_issuer}:aud" = "sts.amazonaws.com"
            "${local.github_issuer}:sub" = subject
          }
        }
      }]
    }
  }
}

# The most the deployer, or any role a scenario creates, can ever do. Scenario
# services in full (S3, CloudFront, ACM for CloudFront's certificates), records
# in the scenarios' zone only, roles under the scenario path only and only
# with this boundary attached, and nothing that touches the bootstrap: its
# state, its roles, this policy or the OIDC provider.
resource "aws_iam_policy" "scenario_boundary" {
  name        = local.boundary_name
  path        = "/artifactweave/"
  description = "Permissions boundary for the deployer and every role an artifactweave scenario creates."
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "ScenarioServices"
        Effect = "Allow"
        Action = [
          "s3:*", "cloudfront:*", "acm:*",
          "route53:Get*", "route53:List*",
          "tag:GetResources", "budgets:ViewBudget", "sts:GetCallerIdentity",
        ]
        Resource = "*"
      },
      {
        Sid      = "ZoneRecords"
        Effect   = "Allow"
        Action   = ["route53:ChangeResourceRecordSets", "route53:ChangeTagsForResource"]
        Resource = local.zone_arn
      },
      {
        Sid      = "ScenarioRoles"
        Effect   = "Allow"
        Action   = concat(local.scenario_role_actions, ["iam:PassRole"])
        Resource = local.scenario_role_arn
      },
      {
        Sid      = "NoRoleWithoutThisBoundary"
        Effect   = "Deny"
        Action   = ["iam:CreateRole", "iam:PutRolePermissionsBoundary"]
        Resource = "*"
        Condition = {
          StringNotEquals = { "iam:PermissionsBoundary" = local.boundary_arn }
        }
      },
      {
        Sid      = "NoBoundaryRemoval"
        Effect   = "Deny"
        Action   = "iam:DeleteRolePermissionsBoundary"
        Resource = "*"
      },
      {
        # Object access to scenarios/ stays open (the scenario state lives
        # there); the bucket itself can only be listed and read.
        Sid    = "StateBucketStaysAsItIs"
        Effect = "Deny"
        NotAction = [
          "s3:GetBucketLocation", "s3:GetBucketVersioning", "s3:ListBucket", "s3:ListBucketVersions",
        ]
        Resource = local.state_bucket_arn
      },
      {
        Sid      = "BootstrapStateIsTheOperators"
        Effect   = "Deny"
        Action   = "s3:*"
        Resource = "${local.state_bucket_arn}/bootstrap/*"
      },
    ]
  })
}

# What the deployer is granted, inside the boundary: buckets with the scenario
# prefix (the state bucket matches it, and the boundary keeps that one to
# objects under scenarios/), CloudFront and its certificates, the scenarios'
# zone, and roles under the scenario path. Read-only inventory calls let the
# workflow's reap action find what a run left behind.
resource "aws_iam_role_policy" "deployer" {
  name = "artifactweave-deployer"
  role = aws_iam_role.deployer.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ScenarioBuckets"
        Effect   = "Allow"
        Action   = "s3:*"
        Resource = ["arn:aws:s3:::${var.scenario_bucket_prefix}*", "arn:aws:s3:::${var.scenario_bucket_prefix}*/*"]
      },
      {
        Sid      = "Inventory"
        Effect   = "Allow"
        Action   = ["s3:ListAllMyBuckets", "tag:GetResources", "budgets:ViewBudget", "sts:GetCallerIdentity"]
        Resource = "*"
      },
      {
        Sid      = "Delivery"
        Effect   = "Allow"
        Action   = ["cloudfront:*", "acm:*"]
        Resource = "*"
      },
      {
        Sid    = "ZoneRecords"
        Effect = "Allow"
        Action = [
          "route53:ChangeResourceRecordSets", "route53:ChangeTagsForResource", "route53:GetHostedZone",
          "route53:ListResourceRecordSets", "route53:ListTagsForResource",
        ]
        Resource = local.zone_arn
      },
      {
        Sid      = "ZoneLookups"
        Effect   = "Allow"
        Action   = ["route53:GetChange", "route53:ListHostedZones", "route53:ListHostedZonesByName"]
        Resource = "*"
      },
      {
        Sid      = "ScenarioRoles"
        Effect   = "Allow"
        Action   = local.scenario_role_actions
        Resource = local.scenario_role_arn
      },
      {
        # S3 replication assumes its role; nothing else may be handed one.
        Sid       = "PassScenarioRolesToS3"
        Effect    = "Allow"
        Action    = "iam:PassRole"
        Resource  = local.scenario_role_arn
        Condition = { StringEquals = { "iam:PassedToService" = "s3.amazonaws.com" } }
      },
    ]
  })
}
