# One private S3 bucket whose bucket policy grants the IAM role artifactweave
# runs as the bucket's objects: the smallest deployment of artifactweave's S3
# backend.

resource "random_id" "suffix" {
  byte_length = 4
}

module "storage" {
  source = "../../modules/aws/storage"

  name              = "${var.name}-${random_id.suffix.hex}"
  region            = var.region
  principals        = { app = var.app_role_arn }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  tags              = var.tags
}
