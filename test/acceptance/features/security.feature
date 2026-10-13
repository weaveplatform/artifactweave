Feature: Legacy security API
  Users, groups, locks and account policies through /api/security (decision
  0005, research 05), as the JFrog CLI and the legacy Terraform resources
  drive them.

  Background:
    Given the registry is running

  Scenario: An admin manages a user and a group over HTTP
    When I PUT "/artifactory/api/security/users/alice" as admin with body:
      """
      {"name": "alice", "email": "alice@example.com", "password": "Alice-pass1"}
      """
    Then the response status is 201
    When I GET "/artifactory/api/security/users/alice" as admin
    Then the response status is 200
    And the JSON field "email" equals "alice@example.com"
    And the JSON field "status" equals "ENABLED"
    When I GET "/artifactory/api/security/apiKey" as "alice" with password "Alice-pass1"
    Then the response status is 404
    When I PUT "/artifactory/api/security/groups/team" as admin with body:
      """
      {"name": "team", "description": "the team", "userNames": ["alice"]}
      """
    Then the response status is 201
    When I GET "/artifactory/api/security/groups/team?includeUsers=true" as admin
    Then the response status is 200
    And the response body contains "alice"
    When I PUT "/artifactory/api/security/userLockPolicy" as admin with body:
      """
      {"enabled": "true", "loginAttempts": "3"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/security/userLockPolicy" as admin
    Then the JSON field "enabled" equals "true"
    And the JSON field "loginAttempts" equals "3"
    When I DELETE "/artifactory/api/security/users/alice" as admin
    Then the response status is 200
    When I DELETE "/artifactory/api/security/groups/team" as admin
    Then the response status is 200

  Scenario: jf rt group-create, user-create, group-add-users and users-delete
    Given the JFrog CLI is available
    When I run jf "rt group-create jf-group --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt user-create jf-user Jf-pass-123 jf-user@example.com --users-groups jf-group --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/security/groups/jf-group?includeUsers=true" as admin
    Then the response status is 200
    And the response body contains "jf-user"
    When I run jf "rt user-create jf-other Jf-pass-456 jf-other@example.com --url {url} --user admin --password {password}"
    Then the command succeeds
    When I run jf "rt group-add-users jf-group jf-other --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/security/groups/jf-group?includeUsers=true" as admin
    Then the response body contains "jf-other"
    When I run jf "rt users-delete jf-user,jf-other --quiet --url {url} --user admin --password {password}"
    Then the command succeeds
    When I GET "/artifactory/api/security/users/jf-user" as admin
    Then the response status is 404
