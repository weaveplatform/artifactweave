@recipes @p2
Feature: Headless P2 connection uses an explicit server-side read grant
  The TLS front only adds the test certificate; it supplies no credentials.
  Anonymous access needs both the setting and a repository-specific grant.

  Scenario: a fresh P2 client installs only while the repository read grant exists
    Given the registry is running
    And the P2 client can reach the registry
    And the published Eclipse P2 update site is available upstream
    And the P2 recipe passes anonymous requests to the registry
    When I PUT "/artifactory/api/repositories/p2-recipe" as admin with body:
      """
      {"rclass":"remote","packageType":"p2","url":"{upstream}"}
      """
    Then the response status is 200
    Given the P2 repository is "p2-recipe"
    When I run the Eclipse P2 director "install"
    Then the command fails
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      security:
        anonAccessEnabled: true
      """
    Then the response status is 200
    When I GET "/artifactory/p2-recipe/content.jar"
    Then the response status is 401
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name":"p2-recipe-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"p2-recipe":{"include_patterns":["**"]}}}}}
      """
    Then the response status is 201
    Given I clear the Eclipse P2 client state
    When I run the Eclipse P2 director "install"
    Then the command succeeds
    And the installed P2 bundle matches the published SHA256
    And the server answered 200 to GET "/artifactory/p2-recipe/plugins/org.eclipse.glsp.layout.source_2.7.0.jar"
    When I DELETE "/access/api/v2/permissions/p2-recipe-read" as admin
    Then the response status is 204
    Given I clear the Eclipse P2 client state
    When I run the Eclipse P2 director "install"
    Then the command fails
