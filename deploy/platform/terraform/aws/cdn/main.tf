# A private S3 bucket behind a CloudFront distribution, for operators who want
# artifactweave's downloads served from the edge (decision 0029).
# artifactweave writes to the bucket and answers a download from a
# downloadRedirect or cdnRedirect repository with a 302 to a CloudFront URL
# it signs; CloudFront checks the signature and reads the bucket through
# origin access control.

resource "random_id" "suffix" {
  byte_length = 4
}

locals {
  name = "${var.name}-${random_id.suffix.hex}"
}

# S3 allows one policy per bucket, so the distribution's read grant joins the
# storage module's own statements.
module "storage" {
  source = "../../modules/aws/storage"

  name              = local.name
  region            = var.region
  principals        = { app = var.app_role_arn }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  policy_statements = [module.cdn.bucket_policy_statement]
  tags              = var.tags
}

# The bucket's outputs one by one, not module.storage itself: a reference to
# the whole module would wait for its bucket policy, which waits for this
# module's statement.
module "cdn" {
  source = "../../modules/aws/cdn"

  name          = local.name
  origin_bucket = { bucket = module.storage.bucket, arn = module.storage.arn, region = module.storage.region }
  store_prefix  = var.store_prefix
  public_keys   = { artifactweave = var.cloudfront_public_key }
  domain_name   = var.domain_name
  zone_id       = var.zone_id
  price_class   = var.price_class
  tags          = var.tags
}
