Feature: Repositories API
  Repository configuration through the Artifactory REST API (research 02), as
  the JFrog CLI and the Terraform provider drive it.

  Background:
    Given the registry is running

  Scenario: An admin creates, reads, updates and deletes a repository
    When I PUT "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic", "description": "first"}
      """
    Then the response status is 200
    And the response body is "Successfully created repository 'generic-local'"
    When I GET "/artifactory/api/repositories/generic-local" as admin
    Then the response status is 200
    And the JSON field "includesPattern" equals "**/*"
    When I POST "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"description": "second"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/repositories/?type=local" as admin
    Then the response status is 200
    And the JSON field "1.key" equals "generic-local"
    And the JSON field "1.description" equals "second"
    When I DELETE "/artifactory/api/repositories/generic-local" as admin
    Then the response status is 200
    And the JSON field "statusMsg" equals "Repository 'generic-local' and all its content have been removed successfully."
    When I GET "/artifactory/api/repositories/generic-local" as admin
    Then the response status is 404

  @jf
  Scenario: jf rt repo-create, repo-update and repo-delete from templates
    Given the JFrog CLI is available
    And a file "create.json" with content:
      """
      {"key": "jf-generic", "rclass": "local", "packageType": "generic"}
      """
    And a file "update.json" with content:
      """
      {"key": "jf-generic", "rclass": "local", "packageType": "generic", "description": "updated by jf"}
      """
    When I run jf "rt repo-create {dir}/create.json --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt repo-update {dir}/update.json --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/repositories/jf-generic" as admin
    Then the JSON field "description" equals "updated by jf"
    When I run jf "rt repo-delete jf-generic --quiet --url {url} --user admin --password {password}"
    Then the command succeeds
