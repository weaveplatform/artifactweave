# A private Cloud Storage bucket behind Cloud CDN, for operators who want
# artifactweave's downloads served from Google's edge (decision 0029).
# artifactweave writes to the bucket and answers a download from a
# downloadRedirect or cdnRedirect repository with a 302 to a Cloud CDN URL it
# signs; Cloud CDN checks the signature and fills its cache from the bucket
# as its fill account, the one reader the bucket admits.

resource "random_id" "suffix" {
  byte_length = 4
}

locals {
  name = "${var.name}-${random_id.suffix.hex}"
}

module "storage" {
  source = "../../modules/gcp/storage"

  name              = local.name
  location          = var.location
  principals        = { app = var.app_service_account }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  labels            = var.labels
}

# The fill account's grant waits for the signing key: Google creates the
# account with a project's first key, which is added with gcloud after the
# first apply so it never reaches the state (decision 0030, Keys).
module "cdn" {
  source = "../../modules/gcp/cdn"

  name           = local.name
  origin_bucket  = module.storage.bucket
  store_prefix   = var.store_prefix
  domain_name    = var.domain_name
  dns_zone       = var.dns_zone
  grant_cdn_fill = var.signed_url_key_added
  labels         = var.labels
}
