@maven @federated_cleanup
Feature: Federated cleanup respects a member's execution direction
  An operator can run ordinary package cleanup on a self-only bidirectional
  repository. A receiving-only member keeps policy administration and content
  but does not execute local cleanup. Multi-member observation and SQL race
  qualification live in the peer and retention suites; this feature claims no
  atomic view of remote downloads.

  Scenario Outline: A self-member's direction controls native cleanup effects
    Given the registry is running
    When I PUT "/artifactory/api/repositories/cleanup-fed" as admin with body:
      """
      {"rclass":"federated","packageType":"maven","members":[{"url":"{url}/cleanup-fed","mode":"<mode>"}]}
      """
    Then the response status is 200
    When I PUT "/artifactory/cleanup-fed/com/example/lib/1.0/lib-1.0.pom" as admin with body:
      """
      <project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>lib</artifactId><version>1.0</version></project>
      """
    Then the response status is 201
    When I PUT "/artifactory/cleanup-fed/com/example/lib/1.0/lib-1.0.jar" as admin with body:
      """
      jar-1.0
      """
    Then the response status is 201
    When I PUT "/artifactory/cleanup-fed/com/example/lib/2.0/lib-2.0.pom" as admin with body:
      """
      <project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>lib</artifactId><version>2.0</version></project>
      """
    Then the response status is 201
    When I PUT "/artifactory/cleanup-fed/com/example/lib/2.0/lib-2.0.jar" as admin with body:
      """
      jar-2.0
      """
    Then the response status is 201
    When I POST "/artifactory/api/cleanup/packages/policies/federated-operator" as admin with body:
      """
      {"key":"federated-operator","description":"initial","searchCriteria":{"packageTypes":["maven"],"repos":["cleanup-fed"],"includedPackages":["**"],"includedProjects":["default"],"keepLastNVersions":1}}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/cleanup/packages/policies/federated-operator" as admin with body:
      """
      {"key":"federated-operator","description":"operator can update this policy","searchCriteria":{"packageTypes":["maven"],"repos":["cleanup-fed"],"includedPackages":["**"],"includedProjects":["default"],"keepLastNVersions":1}}
      """
    Then the response status is 200
    When I GET "/artifactory/api/cleanup/packages/policies/federated-operator" as admin
    Then the response status is 200
    And the JSON field "description" equals "operator can update this policy"
    When I POST "/artifactory/api/cleanup/packages/policies/federated-operator/enablement" as admin with body:
      """
      {"enabled":true}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/cleanup/packages/policies/federated-operator/run" as admin
    Then the response status is 202
    And the JSON field "triggerId" exists
    When within 20 seconds the JSON field "runViews" of GET "/artifactory/api/cleanup/policies/runs/view?policyKey=federated-operator&status=COMPLETED" as admin has 1 entries
    Then the response status is 200
    And the JSON field "runViews.0.status" equals "COMPLETED"
    And the JSON field "runViews.0.totalItems" is the number <items>
    And the JSON field "runViews.0.cleanedItems" is the number <items>
    When I HEAD "/artifactory/cleanup-fed/com/example/lib/1.0/lib-1.0.jar" as admin
    Then the response status is <old_status>
    When I HEAD "/artifactory/cleanup-fed/com/example/lib/1.0/lib-1.0.pom" as admin
    Then the response status is <old_status>
    When I GET "/artifactory/cleanup-fed/com/example/lib/2.0/lib-2.0.jar" as admin
    Then the response status is 200
    And the response body is "jar-2.0"
    When I GET "/artifactory/cleanup-fed/com/example/lib/maven-metadata.xml" as admin
    Then the response status is 200
    And the response body contains "<version>2.0</version>"
    And the response body <old_metadata> "<version>1.0</version>"
    When I DELETE "/artifactory/api/cleanup/packages/policies/federated-operator" as admin
    Then the response status is 200
    When I GET "/artifactory/api/cleanup/packages/policies/federated-operator" as admin
    Then the response status is 404

    Examples:
      | mode          | items | old_status | old_metadata     |
      | bidirectional | 2     | 404        | does not contain |
      | receiver      | 0     | 200        | contains         |
