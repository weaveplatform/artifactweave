@conan
Feature: Native Conan repository endpoint
  Conan clients discover the native API at the repository URL and use
  authenticated revision routes for recipe and binary package transfers.

  Scenario: local and one-node federated Conan endpoints advertise revisions
    Given the registry is running
    When I PUT "/artifactory/api/repositories/conan-local" as admin with body:
      """
      {"rclass":"local","packageType":"conan"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/conan/conan-local/v1/ping" as admin
    Then the response status is 200
    And the response header "X-Conan-Server-Capabilities" contains "revisions"
    When I PUT "/artifactory/api/repositories/conan-fed" as admin with body:
      """
      {"rclass":"federated","packageType":"conan"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/conan/conan-fed/v1/ping" as admin
    Then the response status is 200
    And the response header "X-Conan-Server-Capabilities" contains "revisions"
