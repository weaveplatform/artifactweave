# Addresses from before this root composed modules/gcp/storage (#185). An
# instance applied earlier, such as the lab's conformance environment in an
# operator's local state, keeps its bucket and grant: the plan moves them.

# The bucket.
moved {
  from = google_storage_bucket.blobs
  to   = module.bucket.google_storage_bucket.blobs
}

# The app service account's object rights on it.
moved {
  from = google_storage_bucket_iam_member.app
  to   = module.bucket.google_storage_bucket_iam_member.app["app"]
}
