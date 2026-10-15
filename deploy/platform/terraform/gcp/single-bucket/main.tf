# One private Cloud Storage bucket for artifactweave's gcs backend, with
# object rights for the service account artifactweave runs as. The bucket's
# baseline (no public access, uniform access, soft delete off, abandoned
# upload parts expired) is modules/gcp/storage's.

resource "random_id" "suffix" {
  byte_length = 4
}

module "bucket" {
  source = "../../modules/gcp/storage"

  name              = "${var.name}-${random_id.suffix.hex}"
  location          = var.location
  principals        = { app = var.app_service_account }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  labels            = var.labels
}
