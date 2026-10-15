# Resources that exist before this root: adopted, not recreated.
#
# - The DNS zone was created by CLI so its name servers could be delegated at
#   GoDaddy before this root existed.
# - The workload identity pool and provider were created by the old
#   acceptance root and deleted with it on 2026-10-14. A deleted pool's ID
#   stays reserved for 30 days, so it is undeleted first
#   (`make undelete-pool`), then imported, keeping the provider name the
#   CONFORMANCE_GCP_WIF_PROVIDER variable holds.

import {
  to = google_dns_managed_zone.gcp
  id = "projects/${var.project_id}/managedZones/${var.dns_zone}"
}

import {
  to = google_iam_workload_identity_pool.github
  id = "projects/${var.project_id}/locations/global/workloadIdentityPools/github-actions"
}

import {
  to = google_iam_workload_identity_pool_provider.github
  id = "projects/${var.project_id}/locations/global/workloadIdentityPools/github-actions/providers/artifactweave"
}
