# Repositories whose downloads artifactweave redirects instead of streaming
# (decision 0029), with the jfrog/artifactory provider: one with
# cdnRedirect and one with downloadRedirect. On a store with a CDN, such as
# the deploy/platform cdn scenarios render, both answer a download of 1 MiB
# or more with a 302 to the CDN; without one, downloadRedirect goes to a
# signed storage URL and cdnRedirect streams. The provider describes both
# flags as Enterprise+ only; artifactweave honours them on every instance.

resource "artifactory_local_generic_repository" "cdn" {
  key          = var.cdn_repository_key
  description  = "managed by Terraform: downloads redirect to the CDN"
  cdn_redirect = true
}

resource "artifactory_local_generic_repository" "direct" {
  key             = var.direct_repository_key
  description     = "managed by Terraform: downloads redirect to the CDN or a signed storage URL"
  download_direct = true
}
