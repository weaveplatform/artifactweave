Feature: Permission APIs
  The v1 and v2 permission target APIs and the Access v2 permissions API
  read and write one stored permission (decision 0006), as the JFrog CLI and
  the artifactory_permission_target and platform_permission Terraform
  resources drive them.

  Background:
    Given the registry is running
    When I PUT "/artifactory/api/repositories/perm-accept" as admin with body:
      """
      {"key": "perm-accept", "rclass": "local", "packageType": "generic"}
      """

  # The template is the CLI's own form (what jf rt ptt writes): comma-separated
  # strings, which the CLI turns into the v2 document it sends.
  Scenario: jf rt permission-target-create, -update and -delete
    Given the JFrog CLI is available
    And a file "pt.json" with content:
      """
      {"name": "jf-perm", "repo": {"repositories": "perm-accept", "include-patterns": "**",
        "actions-users": {"admin": "read,write"}}}
      """
    And a file "pt-update.json" with content:
      """
      {"name": "jf-perm", "repo": {"repositories": "perm-accept", "include-patterns": "org/**",
        "actions-users": {"admin": "read,write,manage"}}}
      """
    When I run jf "rt permission-target-create {dir}/pt.json --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/security/permissions/jf-perm" as admin
    Then the response status is 200
    And the JSON field "principals.users.admin" equals "[r w]"
    When I run jf "rt permission-target-update {dir}/pt-update.json --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/access/api/v2/permissions/jf-perm" as admin
    Then the response status is 200
    And the JSON field "resources.artifact.actions.users.admin" equals "[READ WRITE MANAGE]"
    And the JSON field "resources.artifact.targets.perm-accept.include_patterns" equals "[org/**]"
    When I run jf "rt permission-target-delete jf-perm --quiet --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/v2/security/permissions/jf-perm" as admin
    Then the response status is 404

  Scenario: An Access permission keeps the sections v1 and v2 cannot show
    When I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name": "platform-perm", "resources": {
        "artifact": {"actions": {"users": {"admin": ["READ", "MANAGE"]}},
          "targets": {"perm-accept": {"include_patterns": ["**"], "exclude_patterns": []}}},
        "destination": {"actions": {"users": {"admin": ["READ"]}},
          "targets": {"*": {"include_patterns": ["**"]}}}}}
      """
    Then the response status is 201
    And the JSON field "resources.destination.actions.users.admin" equals "[READ]"
    When I GET "/artifactory/api/security/permissions/platform-perm" as admin
    Then the response status is 200
    And the JSON field "principals.users.admin" equals "[r m]"
    When I PUT "/artifactory/api/v2/security/permissions/platform-perm" as admin with body:
      """
      {"name": "platform-perm", "repo": {"repositories": ["perm-accept"], "include-patterns": ["**"],
        "exclude-patterns": [], "actions": {"users": {"admin": ["read"]}}}}
      """
    Then the response status is 200
    When I GET "/access/api/v2/permissions/platform-perm" as admin
    Then the JSON field "resources.artifact.actions.users.admin" equals "[READ]"
    And the JSON field "resources.destination.actions.users.admin" equals "[READ]"
    When I PATCH "/access/api/v2/permissions/platform-perm/artifact" as admin with JSON:
      """
      {"actions": {"groups": {"readers": ["READ"]}}}
      """
    Then the response status is 200
    And the JSON field "actions.groups.readers" equals "[READ]"
    And the JSON field "actions.users.admin" equals "[READ]"
    When I DELETE "/access/api/v2/permissions/platform-perm" as admin
    Then the response status is 204

  Scenario: The seeded permissions read back in every format
    When I GET "/artifactory/api/security/permissions/Anything" as admin
    Then the response status is 200
    And the JSON field "repositories" equals "[ANY]"
    And the JSON field "principals.groups.readers" equals "[r]"
    When I GET "/artifactory/api/v2/security/permissions/Any%20Remote" as admin
    Then the response status is 200
    And the JSON field "repo.repositories" equals "[ANY REMOTE]"
    When I GET "/access/api/v2/permissions/Any%20Remote" as admin
    Then the response status is 200
    And the JSON field "resources.artifact.actions.users.anonymous" equals "[READ WRITE]"
    When I GET "/artifactory/api/security/permissions" as admin
    Then the response body contains "Any Remote"
