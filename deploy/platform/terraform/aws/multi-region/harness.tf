# Outputs only the test harness reads (decision 0030, S5). A consumer can
# delete this file.

output "expires_at" {
  description = "For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise."
  value       = lookup(var.tags, "expires-at", "")
}

output "mirror_members" {
  description = "For the test harness: the mirror's members in order (this instance's zone first), each with its store name, zone, and what a presigned URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it."
  value = [
    for r in var.regions : {
      store             = local.members[r].store
      zone              = r
      signed_url_prefix = module.member[r].signed_url_prefix
    }
  ]
}
