@sbt @maven @client_recipes
Feature: The sbt recipe publishes and resolves using real native clients
  Each invocation has a separate home and Coursier cache. The only shared
  files are the pinned sbt tool bootstrap, never the controlled dependency.

  Scenario Outline: sbt publishes and resolves through a <class> repository
    Given the registry is running
    And the sbt client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"sbt"}
      """
    Then the response status is 200
    When sbt publishes the controlled recipe to "<repo>"
    Then the command succeeds
    And the server answered 201 to PUT "/artifactory/<repo>/org/weave/recipe/widget/1.0.0/widget-1.0.0.jar"
    When sbt resolves the controlled recipe from "<repo>" with a fresh home
    Then the command succeeds
    And the output contains "verified controlled sbt artifact bytes"
    And the server answered 200 to GET "/artifactory/<repo>/org/weave/recipe/widget/1.0.0/widget-1.0.0.jar"
    When sbt cannot resolve the controlled recipe from "<repo>" with invalid credentials
    Then the command fails
    And the server answered 401 to GET "/artifactory/<repo>/org/weave/recipe/widget/1.0.0/widget-1.0.0.pom"

    Examples:
      | class     | repo    |
      | local     | sbt-l   |
      | federated | sbt-f   |

  Scenario: sbt resolves through a virtual and a remote cache from empty client caches
    Given the registry is running
    And the sbt client can reach the registry
    When I PUT "/artifactory/api/repositories/sbt-source" as admin with body:
      """
      {"rclass":"local","packageType":"sbt"}
      """
    Then the response status is 200
    When sbt publishes the controlled recipe to "sbt-source"
    Then the command succeeds
    When I PUT "/artifactory/api/repositories/sbt-v" as admin with body:
      """
      {"rclass":"virtual","packageType":"sbt","repositories":["sbt-source"]}
      """
    Then the response status is 200
    When sbt resolves the controlled recipe from "sbt-v" with a fresh home
    Then the command succeeds
    And the output contains "verified controlled sbt artifact bytes"
    And the server answered 200 to GET "/artifactory/sbt-v/org/weave/recipe/widget/1.0.0/widget-1.0.0.jar"
    When I PUT "/artifactory/api/repositories/sbt-r" as admin with body:
      """
      {"rclass":"remote","packageType":"sbt","url":"{platform}/artifactory/sbt-source","username":"admin","password":"{password}"}
      """
    Then the response status is 200
    When sbt resolves the controlled recipe from "sbt-r" with a fresh home
    Then the command succeeds
    And the output contains "verified controlled sbt artifact bytes"
    And the server answered 200 to GET "/artifactory/sbt-r/org/weave/recipe/widget/1.0.0/widget-1.0.0.jar"
    When I POST "/artifactory/api/repositories/sbt-r" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    When sbt resolves the controlled recipe from "sbt-r" with a fresh home
    Then the command succeeds
    And the output contains "verified controlled sbt artifact bytes"
