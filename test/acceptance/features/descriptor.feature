Feature: Configuration descriptor
  Artifactory's YAML descriptor applied as a patch (decision 0009), as
  configuration-as-code pipelines and the artifactory_* configuration
  resources send it.

  Background:
    Given the registry is running

  Scenario: An admin creates, reapplies and deletes repositories through the descriptor
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        desc-local:
          type: generic
          description: from the descriptor
      virtualRepositories:
        desc-virtual:
          type: generic
          repositories:
            - desc-local
      """
    Then the response status is 200
    And the response body contains "Created: localRepositories.desc-local, virtualRepositories.desc-virtual"
    When I GET "/artifactory/api/repositories/desc-local" as admin
    Then the response status is 200
    And the JSON field "description" equals "from the descriptor"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        desc-local:
          type: generic
          description: from the descriptor
      """
    Then the response status is 200
    And the response body contains "Unchanged: localRepositories.desc-local"
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        desc-local: null
      """
    Then the response status is 200
    And the response body contains "Deleted: localRepositories.desc-local"
    When I GET "/artifactory/api/repositories/desc-local" as admin
    Then the response status is 404

  Scenario: An empty descriptor is refused rather than deleting every repository
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      # nothing
      """
    Then the response status is 400
    And the JSON field "errors.0.message" exists
