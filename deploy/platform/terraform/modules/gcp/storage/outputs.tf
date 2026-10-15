output "bucket" {
  description = "The bucket's name, for blob.gcs.bucket."
  value       = google_storage_bucket.blobs.name
}

output "signed_url_prefix" {
  description = "What a signed URL for an object in this bucket starts with after the scheme: path-style, as the backend signs them."
  value       = "storage.googleapis.com/${google_storage_bucket.blobs.name}/"
}
