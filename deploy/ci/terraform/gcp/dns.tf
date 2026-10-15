# gcp.artifact.weaveplatform.run, delegated from GoDaddy (which serves
# weaveplatform.run) by NS records naming this zone's name servers. Created
# by CLI on 2026-10-14 and imported here (imports.tf). Recreating the zone
# would assign new name servers and silently break the delegation, so
# Terraform refuses to destroy it.
#
# Accepted trivy finding (deploy/check.sh), with its reason:
# - GCP-0013: no DNSSEC. Signing would only help with a DS record in the
#   parent zone at GoDaddy, and the zone serves short-lived lab hostnames;
#   turning it on later is an in-place change plus that DS record.
#trivy:ignore:GCP-0013
resource "google_dns_managed_zone" "gcp" {
  name        = var.dns_zone
  dns_name    = "gcp.artifact.weaveplatform.run."
  description = "artifactweave bootstrap zone; import into Terraform in #126"
  visibility  = "public"
  labels = {
    managed-by = "artifactweave-bootstrap"
    issue      = "126"
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.this]
}
