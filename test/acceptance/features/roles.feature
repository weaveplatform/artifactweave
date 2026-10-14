Feature: Global roles and environments
  Platform admins manage the global environments repositories belong to and
  the global roles project members are given (Access API, issue #77).

  Background:
    Given the registry is running

  Scenario: The built-in environments and roles
    When I GET "/access/api/v1/environments" as admin
    Then the response status is 200
    And the response body contains "DEV"
    And the response body contains "PROD"
    When I GET "/access/api/v1/roles/Project%20Admin" as admin
    Then the response status is 200
    And the JSON field "type" equals "ADMIN"
    When I DELETE "/access/api/v1/environments/DEV" as admin
    Then the response status is 400
    When I DELETE "/access/api/v1/roles/Viewer" as admin
    Then the response status is 400
    When I PUT "/access/api/v1/roles/Project%20Admin" as admin with body:
      """
      {"name": "Project Admin", "type": "ADMIN", "actions": ["READ_REPOSITORY"]}
      """
    Then the response status is 400

  Scenario: A predefined role's actions and environments can change
    When I PUT "/access/api/v1/roles/Viewer" as admin with body:
      """
      {"name": "Viewer", "type": "PREDEFINED", "environments": ["PROD"], "actions": ["READ_REPOSITORY", "READ_BUILD"]}
      """
    Then the response status is 200
    When I GET "/access/api/v1/roles/Viewer" as admin
    Then the response status is 200
    And the JSON field "type" equals "PREDEFINED"
    And the response body contains "READ_BUILD"

  Scenario: A custom global role in a custom environment
    When I POST "/access/api/v1/environments" as admin with body:
      """
      {"name": "QA"}
      """
    Then the response status is 201
    When I POST "/access/api/v1/roles" as admin with body:
      """
      {"name": "qa-readers", "type": "CUSTOM_GLOBAL", "environments": ["qa"], "actions": ["READ_REPOSITORY", "READ_BUILD"]}
      """
    Then the response status is 201
    And the JSON field "code" equals "OK"
    And the JSON field "message" equals "Role 'qa-readers' created"
    When I POST "/access/api/v1/environments/QA/rename" as admin with body:
      """
      {"new_name": "STAGE"}
      """
    Then the response status is 200
    When I GET "/access/api/v1/roles/qa-readers" as admin
    Then the response status is 200
    And the response body contains "STAGE"
    When I PUT "/access/api/v1/roles/qa-readers" as admin with body:
      """
      {"name": "qa-readers", "type": "CUSTOM_GLOBAL", "actions": ["READ_REPOSITORY"]}
      """
    Then the response status is 200
    When I DELETE "/access/api/v1/roles/qa-readers" as admin
    Then the response status is 204
    When I DELETE "/access/api/v1/environments/STAGE" as admin
    Then the response status is 204
