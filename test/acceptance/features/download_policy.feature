Feature: Downloads refused by a repository's configuration
  A blacked-out repository serves nothing, and a path outside its include and
  exclude patterns is not served, both answered 404 as Artifactory answers them
  (decision 0032, #328).

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    And I PUT "/artifactory/generic-local/keep/a.txt" as admin with body:
      """
      kept
      """
    And I PUT "/artifactory/generic-local/keep/a.tmp" as admin with body:
      """
      scratch
      """

  Scenario: An excluded path and a blacked-out repository answer 404 over HTTP
    When I POST "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"excludesPattern": "**/*.tmp"}
      """
    Then the response status is 200
    When I GET "/artifactory/generic-local/keep/a.tmp" as admin
    Then the response status is 404
    And the response body contains "rejected the artifact 'generic-local:keep/a.tmp' due to its include/exclude pattern settings"
    When I GET "/artifactory/generic-local/keep/a.txt" as admin
    Then the response status is 200
    And the response body is "kept"
    When I POST "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"blackedOut": true}
      """
    Then the response status is 200
    When I GET "/artifactory/generic-local/keep/a.txt" as admin
    Then the response status is 404
    And the response body contains "The repository 'generic-local' is blacked out and cannot serve artifact 'generic-local:keep/a.txt'."
    When I GET "/artifactory/api/storage/generic-local/keep/a.txt" as admin
    Then the response status is 200

  @jf
  Scenario: jf rt dl fails on an excluded path and on a blacked-out repository
    Given the JFrog CLI is available
    And a file "a.txt" with content:
      """
      kept
      """
    When I POST "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"excludesPattern": "**/*.tmp"}
      """
    Then the response status is 200
    When I run jf "rt dl generic-local/keep/a.tmp {dir}/out/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command fails
    And the server answered 404 to GET "/artifactory/generic-local/keep/a.tmp"
    And the file "out/a.tmp" does not exist
    When I run jf "rt dl generic-local/keep/a.txt {dir}/out/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "a.txt" and "out/a.txt" are identical
    When I POST "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"blackedOut": true}
      """
    Then the response status is 200
    When I run jf "rt dl generic-local/keep/a.txt {dir}/again/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command fails
    And the server answered 404 to GET "/artifactory/generic-local/keep/a.txt"
    And the file "again/a.txt" does not exist
