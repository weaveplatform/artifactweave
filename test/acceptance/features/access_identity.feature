Feature: Access v2 users and groups
  Users, groups, the basic authentication switch and the password policy
  through /access/api/v2 and /access/api/v1/config (decision 0005), as the
  jfrog/artifactory and jfrog/platform Terraform providers drive them.

  Background:
    Given the registry is running

  Scenario: An admin manages a user and a group through Access v2
    When I POST "/access/api/v2/users" as admin with body:
      """
      {"username": "v2-alice", "email": "alice@example.com", "password": "Alice-pass1"}
      """
    Then the response status is 201
    And the JSON field "realm" equals "internal"
    When I GET "/access/api/v2/users/v2-alice" as "v2-alice" with password "Alice-pass1"
    Then the response status is 200
    And the JSON field "email" equals "alice@example.com"
    When I POST "/access/api/v2/groups" as admin with body:
      """
      {"name": "v2-team", "description": "the team", "members": ["v2-alice"]}
      """
    Then the response status is 201
    When I PATCH "/access/api/v2/groups/v2-team/members" as admin with JSON:
      """
      {"remove": ["v2-alice"]}
      """
    Then the response status is 200
    When I PATCH "/access/api/v2/users/v2-alice/groups" as admin with JSON:
      """
      {"add": ["v2-team"]}
      """
    Then the response status is 200
    And the response body contains "v2-team"
    When I GET "/access/api/v2/users?username=v2-" as admin
    Then the response status is 200
    And the response body contains "/access/api/v2/users/v2-alice"
    When I DELETE "/access/api/v2/users/v2-alice" as admin
    Then the response status is 204
    When I DELETE "/access/api/v2/groups/v2-team" as admin
    Then the response status is 204

  Scenario: The password policy applies to new passwords
    When I PUT "/access/api/v1/config/security/password_policy" as admin with body:
      """
      {"length": 12, "digit": 1}
      """
    Then the response status is 204
    When I POST "/access/api/v2/users" as admin with body:
      """
      {"username": "weak", "email": "weak@example.com", "password": "Short1"}
      """
    Then the response status is 400
    When I POST "/access/api/v2/users" as admin with body:
      """
      {"username": "strong", "email": "strong@example.com", "password": "Long-enough-pw1"}
      """
    Then the response status is 201

  Scenario: With basic authentication disabled, passwords fail and tokens work
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in": 3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "admin-token"
    When I PUT "/access/api/v1/config/security/authentication/basic_authentication_enabled" as admin with body:
      """
      {"enabled": false}
      """
    Then the response status is 200
    When I GET "/access/api/v2/users" as admin
    Then the response status is 401
    When I GET "/access/api/v1/config/security/authentication/basic_authentication_enabled" with the kept token "admin-token"
    Then the response status is 200
    And the JSON field "enabled" equals "false"
