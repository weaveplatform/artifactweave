# Two S3 buckets in two regions as members of one artifactweave mirror
# (decision 0028): every blob is written to both, an upload succeeds once
# write_quorum of them hold it, and reads prefer the member in this
# instance's zone. The reference for regional resilience under artifactweave's
# control. Neither bucket replicates on its own: cross-region replication's
# copies are invisible to the mirror and never count toward its quorum. One
# provider serves both regions through the per-resource region of the AWS
# provider 6. GitHub's OIDC provider and the conformance role live in
# aws/bootstrap; apply it first.

# The role artifactweave runs as. Locally that is the data-plane role
# aws/bootstrap created. A scenario run names the deployer instead: it applies
# this root and runs artifactweave in the same job, and the boundary keeps it
# from reading the bootstrap's roles, so the lookup is skipped when the ARN is
# given.
data "aws_iam_role" "conformance" {
  count = var.app_role_arn == "" ? 1 : 0
  name  = var.conformance_role_name
}

resource "random_id" "suffix" {
  byte_length = 4
}

locals {
  app_role_arn = var.app_role_arn != "" ? var.app_role_arn : data.aws_iam_role.conformance[0].arn
  # The mirror's members in order: this instance's zone first.
  regions = [var.primary_region, var.secondary_region]
  members = {
    for r in local.regions : r => {
      store = "s3_${replace(r, "-", "_")}"
      # eu-west-2 becomes euw2, so a scenario bucket fits S3's 63
      # characters: artifactweave-awscn- (20), the instance, the suffix (8)
      # and the region's code with their hyphens leave 29 for the instance,
      # which a run tag such as pr160-38712345678-mr-t (22) meets. The
      # module refuses a longer name at plan.
      bucket = "${var.name}-${random_id.suffix.hex}-${replace(r, "/^([a-z]+)-([a-z])[a-z]*-([0-9]+)$/", "$1$2$3")}"
    }
  }
}

module "member" {
  source   = "../../modules/aws/storage"
  for_each = local.members

  name         = each.value.bucket
  region       = each.key
  store_prefix = var.store_prefix
  app_role_arn = local.app_role_arn
  tags         = var.scenario_labels
}
