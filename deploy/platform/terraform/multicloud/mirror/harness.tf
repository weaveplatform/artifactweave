# Outputs only the test harness reads (decision 0030, S5). A consumer can
# delete this file.

output "expires_at" {
  description = "For the test harness: the expires-at label (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise."
  value       = lookup(var.labels, "expires-at", "")
}

output "mirror_members" {
  description = "For the test harness: the mirror's members in order (the home cloud's first), each with its store name, zone, and what a signed URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it."
  value = [
    for c in local.order : {
      store             = local.members[c].store
      zone              = local.members[c].config.zone
      signed_url_prefix = local.members[c].signed_url_prefix
    }
  ]
}
