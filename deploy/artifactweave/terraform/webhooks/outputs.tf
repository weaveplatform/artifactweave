output "webhook_keys" {
  description = "The 24 required subscriptions, indexed by exact provider resource type."
  value = {
    artifactory_artifact_webhook                           = artifactory_artifact_webhook.artifact_standard.key
    artifactory_artifact_custom_webhook                    = artifactory_artifact_custom_webhook.artifact_custom.key
    artifactory_artifact_property_webhook                  = artifactory_artifact_property_webhook.artifact_property_standard.key
    artifactory_artifact_property_custom_webhook           = artifactory_artifact_property_custom_webhook.artifact_property_custom.key
    artifactory_artifact_lifecycle_webhook                 = artifactory_artifact_lifecycle_webhook.artifact_lifecycle_standard.key
    artifactory_artifact_lifecycle_custom_webhook          = artifactory_artifact_lifecycle_custom_webhook.artifact_lifecycle_custom.key
    artifactory_docker_webhook                             = artifactory_docker_webhook.docker_standard.key
    artifactory_docker_custom_webhook                      = artifactory_docker_custom_webhook.docker_custom.key
    artifactory_build_webhook                              = artifactory_build_webhook.build_standard.key
    artifactory_build_custom_webhook                       = artifactory_build_custom_webhook.build_custom.key
    artifactory_release_bundle_webhook                     = artifactory_release_bundle_webhook.release_bundle_standard.key
    artifactory_release_bundle_custom_webhook              = artifactory_release_bundle_custom_webhook.release_bundle_custom.key
    artifactory_distribution_webhook                       = artifactory_distribution_webhook.distribution_standard.key
    artifactory_distribution_custom_webhook                = artifactory_distribution_custom_webhook.distribution_custom.key
    artifactory_artifactory_release_bundle_webhook         = artifactory_artifactory_release_bundle_webhook.artifactory_release_bundle_standard.key
    artifactory_artifactory_release_bundle_custom_webhook  = artifactory_artifactory_release_bundle_custom_webhook.artifactory_release_bundle_custom.key
    artifactory_destination_webhook                        = artifactory_destination_webhook.destination_standard.key
    artifactory_destination_custom_webhook                 = artifactory_destination_custom_webhook.destination_custom.key
    artifactory_user_webhook                               = artifactory_user_webhook.user_standard.key
    artifactory_user_custom_webhook                        = artifactory_user_custom_webhook.user_custom.key
    artifactory_release_bundle_v2_webhook                  = artifactory_release_bundle_v2_webhook.release_bundle_v2_standard.key
    artifactory_release_bundle_v2_custom_webhook           = artifactory_release_bundle_v2_custom_webhook.release_bundle_v2_custom.key
    artifactory_release_bundle_v2_promotion_webhook        = artifactory_release_bundle_v2_promotion_webhook.release_bundle_v2_promotion_standard.key
    artifactory_release_bundle_v2_promotion_custom_webhook = artifactory_release_bundle_v2_promotion_custom_webhook.release_bundle_v2_promotion_custom.key
  }
}
