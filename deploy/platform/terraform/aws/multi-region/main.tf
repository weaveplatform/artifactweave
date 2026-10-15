# S3 buckets in two or more regions as members of one artifactweave mirror
# (decision 0028): every blob is written to each, an upload succeeds once
# write_quorum of them hold it, and reads prefer the member in this
# instance's zone. No bucket replicates on its own: cross-region
# replication's copies are invisible to the mirror and never count toward
# its quorum.

resource "random_id" "suffix" {
  byte_length = 4
}

locals {
  # A region code keeps bucket names short: the area, the direction's
  # initials and the number, so eu-central-1 becomes euc1 and ap-southeast-1
  # apse1. One initial per word would make ap-south-1 and ap-southeast-1
  # both aps1.
  directions = {
    north     = "n"
    south     = "s"
    east      = "e"
    west      = "w"
    central   = "c"
    northeast = "ne"
    northwest = "nw"
    southeast = "se"
    southwest = "sw"
  }
  codes = {
    for r in var.regions : r => join("", [
      for i, part in split("-", r) :
      i == 0 || i == length(split("-", r)) - 1 ? part : lookup(local.directions, part, substr(part, 0, 1))
    ])
  }
  members = {
    for r in var.regions : r => {
      store  = "s3_${replace(r, "-", "_")}"
      bucket = "${var.name}-${random_id.suffix.hex}-${local.codes[r]}"
    }
  }
}

module "member" {
  source   = "../../modules/aws/storage"
  for_each = local.members

  name              = each.value.bucket
  region            = each.key
  principals        = { app = var.app_role_arn }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  tags              = var.tags
}
