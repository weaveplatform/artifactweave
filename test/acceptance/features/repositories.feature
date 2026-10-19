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

  Scenario: Reads spell package types and empty fields as Artifactory 7.x does
    When I PUT "/artifactory/api/repositories/generic-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/rpm-local" as admin with body:
      """
      {"rclass": "local", "packageType": "rpm"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/repositories?packageType=generic" as admin
    Then the response status is 200
    And the JSON field "0.key" equals "generic-local"
    And the JSON field "0.packageType" equals "Generic"
    When I GET "/artifactory/api/repositories?packageType=yum" as admin
    Then the JSON field "0.key" equals "rpm-local"
    And the JSON field "0.packageType" equals "YUM"
    When I GET "/artifactory/api/v2/repositories/generic-local" as admin
    Then the response status is 200
    And the JSON field "description" equals ""
    And the JSON field "notes" equals ""
    And the JSON field "propertySets" has 0 entries
    And the response body does not contain "checksumPolicyType"
    And the response body does not contain "forceMetadataNameVersion"

  Scenario: Repository keys follow the naming rules on every write path
    When I PUT "/artifactory/api/repositories/release-bundles" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 400
    And the response body contains "is a reserved repository key"
    When I PUT "/artifactory/api/repositories/a:b" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 400
    When I PUT "/artifactory/api/repositories/remote-key-of-fifty-nine-characters-xxxxxxxxxxxxxxxxxxxxxxx" as admin with body:
      """
      {"rclass": "remote", "packageType": "generic", "url": "https://example.com"}
      """
    Then the response status is 400
    And the response body contains "at most 58 characters"
    When I PUT "/artifactory/api/repositories/1st-generic" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/v2/repositories/batch" as admin with body:
      """
      [{"key": "batch-ok", "rclass": "local"}, {"key": "Jfrog-system-reserved-x", "rclass": "local"}]
      """
    Then the response status is 400
    When I GET "/artifactory/api/repositories/batch-ok" as admin
    Then the response status is 404
    When I PATCH "/artifactory/api/system/configuration" as admin with YAML:
      """
      localRepositories:
        oci_local:
          type: oci
      """
    Then the response status is 400
    When I PUT "/artifactory/api/repositories/oci-local" as admin with body:
      """
      {"rclass": "local", "packageType": "oci"}
      """
    Then the response status is 200
    When I POST "/artifactory/api/repositories/oci-local" as admin with body:
      """
      {"key": "OCI-Local", "description": "respelled"}
      """
    Then the response status is 400
    And the response body contains "must be lower case without an underscore"
    When I POST "/artifactory/api/repositories/oci-local" as admin with body:
      """
      {"key": "oci-local", "description": "kept"}
      """
    Then the response status is 200

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

  Scenario: A virtual repository may have no members, and each member has its package type
    When I PUT "/artifactory/api/repositories/npm-local" as admin with body:
      """
      {"rclass": "local", "packageType": "npm"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/maven-local" as admin with body:
      """
      {"rclass": "local", "packageType": "maven"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/npm-virtual" as admin with body:
      """
      {"rclass": "virtual", "packageType": "npm"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/repositories/npm-virtual" as admin
    Then the response status is 200
    And the JSON field "repositories" has 0 entries
    When I POST "/artifactory/api/repositories/npm-virtual" as admin with body:
      """
      {"repositories": ["maven-local"]}
      """
    Then the response status is 400
    And the response body contains "does not match the virtual repository's package type 'npm'"
    When I POST "/artifactory/api/repositories/npm-virtual" as admin with body:
      """
      {"repositories": ["npm-local"], "defaultDeploymentRepo": "npm-local"}
      """
    Then the response status is 200
    When I DELETE "/artifactory/api/repositories/npm-local" as admin
    Then the response status is 200
    When I POST "/artifactory/api/repositories/npm-virtual" as admin with body:
      """
      {"description": "its last member is gone"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/repositories/npm-virtual" as admin
    Then the JSON field "repositories" has 0 entries
    And the JSON field "description" equals "its last member is gone"
    When I GET "/artifactory/npm-virtual/any/file.txt" as admin
    Then the response status is 404
