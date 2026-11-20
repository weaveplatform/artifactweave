resource "artifactory_artifact_webhook" "artifact_standard" {
  key         = "tf-webhook-artifact_standard"
  description = "artifact_standard revision ${var.revision}"
  enabled     = true
  event_types = ["deployed"]

  criteria {
    any_local        = true
    any_remote       = false
    any_federated    = false
    repo_keys        = []
    include_patterns = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/artifact_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "artifact_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_artifact_custom_webhook" "artifact_custom" {
  key         = "tf-webhook-artifact_custom"
  description = "artifact_custom revision ${var.revision}"
  enabled     = true
  event_types = ["deployed"]

  criteria {
    any_local        = true
    any_remote       = false
    any_federated    = false
    repo_keys        = []
    include_patterns = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/artifact_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "artifact_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "artifact_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_artifact_property_webhook" "artifact_property_standard" {
  key         = "tf-webhook-artifact_property_standard"
  description = "artifact_property_standard revision ${var.revision}"
  enabled     = true
  event_types = ["added"]

  criteria {
    any_local        = true
    any_remote       = false
    any_federated    = false
    repo_keys        = []
    include_patterns = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/artifact_property_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "artifact_property_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_artifact_property_custom_webhook" "artifact_property_custom" {
  key         = "tf-webhook-artifact_property_custom"
  description = "artifact_property_custom revision ${var.revision}"
  enabled     = true
  event_types = ["added"]

  criteria {
    any_local        = true
    any_remote       = false
    any_federated    = false
    repo_keys        = []
    include_patterns = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/artifact_property_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "artifact_property_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "artifact_property_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_artifact_lifecycle_webhook" "artifact_lifecycle_standard" {
  key         = "tf-webhook-artifact_lifecycle_standard"
  description = "artifact_lifecycle_standard revision ${var.revision}"
  enabled     = true
  event_types = ["archive"]

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/artifact_lifecycle_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "artifact_lifecycle_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_artifact_lifecycle_custom_webhook" "artifact_lifecycle_custom" {
  key         = "tf-webhook-artifact_lifecycle_custom"
  description = "artifact_lifecycle_custom revision ${var.revision}"
  enabled     = true
  event_types = ["archive"]

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/artifact_lifecycle_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "artifact_lifecycle_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "artifact_lifecycle_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_docker_webhook" "docker_standard" {
  key         = "tf-webhook-docker_standard"
  description = "docker_standard revision ${var.revision}"
  enabled     = true
  event_types = ["pushed"]

  criteria {
    any_local        = true
    any_remote       = false
    any_federated    = false
    repo_keys        = []
    include_patterns = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/docker_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "docker_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_docker_custom_webhook" "docker_custom" {
  key         = "tf-webhook-docker_custom"
  description = "docker_custom revision ${var.revision}"
  enabled     = true
  event_types = ["pushed"]

  criteria {
    any_local        = true
    any_remote       = false
    any_federated    = false
    repo_keys        = []
    include_patterns = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/docker_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "docker_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "docker_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_build_webhook" "build_standard" {
  key         = "tf-webhook-build_standard"
  description = "build_standard revision ${var.revision}"
  enabled     = true
  event_types = ["uploaded"]

  criteria {
    any_build        = false
    selected_builds  = ["fixture-build"]
    include_patterns = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/build_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "build_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_build_custom_webhook" "build_custom" {
  key         = "tf-webhook-build_custom"
  description = "build_custom revision ${var.revision}"
  enabled     = true
  event_types = ["uploaded"]

  criteria {
    any_build        = false
    selected_builds  = ["fixture-build"]
    include_patterns = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/build_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "build_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "build_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_release_bundle_webhook" "release_bundle_standard" {
  key         = "tf-webhook-release_bundle_standard"
  description = "release_bundle_standard revision ${var.revision}"
  enabled     = true
  event_types = ["signed"]

  criteria {
    any_release_bundle              = false
    registered_release_bundle_names = ["fixture-bundle"]
    include_patterns                = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns                = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/release_bundle_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "release_bundle_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_release_bundle_custom_webhook" "release_bundle_custom" {
  key         = "tf-webhook-release_bundle_custom"
  description = "release_bundle_custom revision ${var.revision}"
  enabled     = true
  event_types = ["signed"]

  criteria {
    any_release_bundle              = false
    registered_release_bundle_names = ["fixture-bundle"]
    include_patterns                = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns                = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/release_bundle_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "release_bundle_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "release_bundle_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_distribution_webhook" "distribution_standard" {
  key         = "tf-webhook-distribution_standard"
  description = "distribution_standard revision ${var.revision}"
  enabled     = true
  event_types = ["distribute_started"]

  criteria {
    any_release_bundle              = false
    registered_release_bundle_names = ["fixture-bundle"]
    include_patterns                = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns                = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/distribution_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "distribution_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_distribution_custom_webhook" "distribution_custom" {
  key         = "tf-webhook-distribution_custom"
  description = "distribution_custom revision ${var.revision}"
  enabled     = true
  event_types = ["distribute_started"]

  criteria {
    any_release_bundle              = false
    registered_release_bundle_names = ["fixture-bundle"]
    include_patterns                = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns                = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/distribution_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "distribution_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "distribution_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_artifactory_release_bundle_webhook" "artifactory_release_bundle_standard" {
  key         = "tf-webhook-artifactory_release_bundle_standard"
  description = "artifactory_release_bundle_standard revision ${var.revision}"
  enabled     = true
  event_types = ["received"]

  criteria {
    any_release_bundle              = false
    registered_release_bundle_names = ["fixture-bundle"]
    include_patterns                = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns                = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/artifactory_release_bundle_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "artifactory_release_bundle_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_artifactory_release_bundle_custom_webhook" "artifactory_release_bundle_custom" {
  key         = "tf-webhook-artifactory_release_bundle_custom"
  description = "artifactory_release_bundle_custom revision ${var.revision}"
  enabled     = true
  event_types = ["received"]

  criteria {
    any_release_bundle              = false
    registered_release_bundle_names = ["fixture-bundle"]
    include_patterns                = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns                = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/artifactory_release_bundle_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "artifactory_release_bundle_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "artifactory_release_bundle_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_destination_webhook" "destination_standard" {
  key         = "tf-webhook-destination_standard"
  description = "destination_standard revision ${var.revision}"
  enabled     = true
  event_types = ["received"]

  criteria {
    any_release_bundle              = false
    registered_release_bundle_names = ["fixture-bundle"]
    include_patterns                = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns                = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/destination_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "destination_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_destination_custom_webhook" "destination_custom" {
  key         = "tf-webhook-destination_custom"
  description = "destination_custom revision ${var.revision}"
  enabled     = true
  event_types = ["received"]

  criteria {
    any_release_bundle              = false
    registered_release_bundle_names = ["fixture-bundle"]
    include_patterns                = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns                = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/destination_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "destination_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "destination_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_user_webhook" "user_standard" {
  key         = "tf-webhook-user_standard"
  description = "user_standard revision ${var.revision}"
  enabled     = true
  event_types = ["locked"]

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/user_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "user_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_user_custom_webhook" "user_custom" {
  key         = "tf-webhook-user_custom"
  description = "user_custom revision ${var.revision}"
  enabled     = true
  event_types = ["locked"]

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/user_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "user_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "user_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_release_bundle_v2_webhook" "release_bundle_v2_standard" {
  key         = "tf-webhook-release_bundle_v2_standard"
  description = "release_bundle_v2_standard revision ${var.revision}"
  enabled     = true
  event_types = ["release_bundle_v2_started"]

  criteria {
    any_release_bundle       = false
    selected_release_bundles = ["fixture-bundle"]
    include_patterns         = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns         = ["**/temporary/**"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/release_bundle_v2_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "release_bundle_v2_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_release_bundle_v2_custom_webhook" "release_bundle_v2_custom" {
  key         = "tf-webhook-release_bundle_v2_custom"
  description = "release_bundle_v2_custom revision ${var.revision}"
  enabled     = true
  event_types = ["release_bundle_v2_started"]

  criteria {
    any_release_bundle       = false
    selected_release_bundles = ["fixture-bundle"]
    include_patterns         = [var.revision == 1 ? "org/**" : "release/**"]
    exclude_patterns         = ["**/temporary/**"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/release_bundle_v2_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "release_bundle_v2_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "release_bundle_v2_custom"
      revision = var.revision
    })
  }
}

resource "artifactory_release_bundle_v2_promotion_webhook" "release_bundle_v2_promotion_standard" {
  key         = "tf-webhook-release_bundle_v2_promotion_standard"
  description = "release_bundle_v2_promotion_standard revision ${var.revision}"
  enabled     = true
  event_types = ["release_bundle_v2_promotion_started"]

  criteria {
    selected_environments = [var.revision == 1 ? "DEV" : "PROD"]
  }

  handler {
    url                    = "${trimsuffix(var.receiver_url, "/")}/release_bundle_v2_promotion_standard/${var.revision}"
    secret                 = var.handler_secret
    use_secret_for_signing = var.revision == 2
    custom_http_headers = {
      "X-Fixture"  = "release_bundle_v2_promotion_standard"
      "X-Revision" = tostring(var.revision)
    }
  }
}

resource "artifactory_release_bundle_v2_promotion_custom_webhook" "release_bundle_v2_promotion_custom" {
  key         = "tf-webhook-release_bundle_v2_promotion_custom"
  description = "release_bundle_v2_promotion_custom revision ${var.revision}"
  enabled     = true
  event_types = ["release_bundle_v2_promotion_started"]

  criteria {
    selected_environments = [var.revision == 1 ? "DEV" : "PROD"]
  }

  handler {
    url    = "${trimsuffix(var.receiver_url, "/")}/release_bundle_v2_promotion_custom/${var.revision}"
    method = var.revision == 1 ? "POST" : "PATCH"
    secrets = {
      credential = var.handler_secret
    }
    http_headers = {
      "X-Fixture"  = "release_bundle_v2_promotion_custom"
      "X-Revision" = tostring(var.revision)
    }
    payload = jsonencode({
      fixture  = "release_bundle_v2_promotion_custom"
      revision = var.revision
    })
  }
}
