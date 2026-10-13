Feature: Storage API: item information, listings and properties
  The storage API views and property edits (research 02) as plain HTTP
  clients, the Terraform provider and the JFrog CLI use them (research 09).

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/props-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    And I PUT "/artifactory/props-local/dir/a.txt" as admin with body:
      """
      alpha
      """

  Scenario: Item information, listings and property edits over HTTP
    When I GET "/artifactory/api/storage/props-local/dir/a.txt" as admin
    Then the response status is 200
    And the JSON field "path" equals "/dir/a.txt"
    And the JSON field "size" equals "5"
    When I GET "/artifactory/api/storage/props-local?list&deep=1&listFolders=1" as admin
    Then the response status is 200
    And the response body contains "/dir/a.txt"
    When I PUT "/artifactory/api/storage/props-local/dir?properties=team=core;env=prod,qa" as admin
    Then the response status is 204
    When I GET "/artifactory/api/storage/props-local/dir/a.txt?properties" as admin
    Then the response status is 200
    And the JSON field "properties.team" equals "[core]"
    And the JSON field "properties.env" equals "[prod qa]"
    When I PATCH "/artifactory/api/metadata/props-local/dir/a.txt" as admin with JSON:
      """
      {"props": {"env": "staging", "team": null}}
      """
    Then the response status is 204
    When I GET "/artifactory/api/storage/props-local/dir/a.txt?properties" as admin
    Then the JSON field "properties.env" equals "[staging]"
    When I DELETE "/artifactory/api/storage/props-local/dir/a.txt?properties=env" as admin
    Then the response status is 204
    When I GET "/artifactory/api/storage/props-local/dir/a.txt?properties" as admin
    Then the response status is 404

  @jf
  Scenario: jf rt sp and jf rt delp
    Given the JFrog CLI is available
    When I run jf "rt sp props-local/dir/* team=core;env=prod --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/storage/props-local/dir/a.txt?properties" as admin
    Then the response status is 200
    And the JSON field "properties.team" equals "[core]"
    And the JSON field "properties.env" equals "[prod]"
    When I run jf "rt delp props-local/dir/* team --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/storage/props-local/dir/a.txt?properties" as admin
    Then the JSON field "properties.env" equals "[prod]"
    When I GET "/artifactory/api/storage/props-local/dir/a.txt?properties=team" as admin
    Then the response status is 404
