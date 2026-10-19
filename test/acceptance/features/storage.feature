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

  Scenario: Properties on the repository itself, as the Terraform provider sets them
    When I GET "/artifactory/api/storage/props-local?properties=" as admin
    Then the response status is 404
    When I PUT "/artifactory/api/storage/props-local?properties=key1=value1,value2;key2=value3&recursive=0" as admin
    Then the response status is 204
    When I GET "/artifactory/api/storage/props-local?properties=" as admin
    Then the response status is 200
    And the JSON field "properties.key1" equals "[value1 value2]"
    And the JSON field "properties.key2" equals "[value3]"
    When I GET "/artifactory/api/storage/props-local/dir/a.txt?properties" as admin
    Then the response status is 404
    When I PATCH "/artifactory/api/metadata/props-local?recursiveProperties=0" as admin with JSON:
      """
      {"props": {"key1": "value1", "key3": "value5", "key2": null}}
      """
    Then the response status is 204
    When I GET "/artifactory/api/storage/props-local?properties=" as admin
    Then the response status is 200
    And the JSON field "properties.key1" equals "[value1]"
    And the JSON field "properties.key3" equals "[value5]"
    When I DELETE "/artifactory/api/storage/props-local?properties=key1,key3&recursive=0" as admin
    Then the response status is 204
    When I GET "/artifactory/api/storage/props-local?properties=" as admin
    Then the response status is 404

  Scenario: The file list as the Terraform artifactory_file_list data source reads it
    The data source sends every listing parameter, and an empty path for
    folder_path "/". It decodes size as an int64, so a string fails it.
    When I PUT "/artifactory/props-local/dir/b.txt;team=core" as admin with body:
      """
      beta
      """
    Then the response status is 201
    When I GET "/artifactory/api/storage/props-local/?deep=1&depth=3&includeRootPath=1&list=&listFolders=1&mdTimestamps=1" as admin
    Then the response status is 200
    And the JSON field "files" has 4 entries
    And the JSON field "files.0.uri" equals "/"
    And the JSON field "files.0.folder" equals "true"
    And the JSON field "files.0.size" is the number -1
    And the JSON field "files.1.uri" equals "/dir"
    And the JSON field "files.1.size" is the number -1
    And the JSON field "files.2.uri" equals "/dir/a.txt"
    And the JSON field "files.2.size" is the number 5
    And the JSON field "files.2.sha1" is not empty
    And the JSON field "files.2.sha2" is not empty
    And the JSON field "files.2.mdTimestamps" does not exist
    And the JSON field "files.3.uri" equals "/dir/b.txt"
    And the JSON field "files.3.size" is the number 4
    And the JSON field "files.3.sha2" is not empty
    And the JSON field "files.3.mdTimestamps.properties" is not empty

  @jf
  Scenario: jf rt sp and jf rt delp on the repository itself
    Given the JFrog CLI is available
    When I run jf "rt sp props-local/ owner=platform --repo-only --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/storage/props-local?properties" as admin
    Then the response status is 200
    And the JSON field "properties.owner" equals "[platform]"
    When I GET "/artifactory/api/storage/props-local/dir/a.txt?properties" as admin
    Then the response status is 404
    When I run jf "rt delp props-local/ owner --repo-only --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/storage/props-local?properties" as admin
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
