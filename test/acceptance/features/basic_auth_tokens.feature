Feature: Creating tokens with a password
  A password sent with Basic creates no token through the Access token API
  until an administrator sets token.allow_basic_auth, as Artifactory 7.x ships
  "Enable Token Generation via API" (token.allow-basic-auth) off. Every other
  feature runs with it on (steps_test.go, start); these run with the default.

  Scenario: With the default, a password reaches only the legacy route, and its token reaches the Access route
    Given the registry is running with environment "ARTIFACTWEAVE_TOKEN_ALLOW_BASIC_AUTH=false"
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 401
    And the response body contains "Unsupported authentication method Basic"
    Given the next request has header "Content-Type" "application/x-www-form-urlencoded"
    When I POST "/artifactory/api/security/token" as admin with body:
      """
      username=admin&scope=member-of-groups:*&expires_in=3600
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "admin-token"
    Given the next request has header "Authorization" "Bearer {admin-token}"
    When I POST "/access/api/v1/tokens" with body:
      """
      {"username":"svc","scope":"applied-permissions/user","expires_in":3600}
      """
    Then the response status is 200
    And the JSON field "token_type" equals "Bearer"

  @jf
  Scenario: With the default, jf configured with a password swaps it for a token
    Given the registry is running with environment "ARTIFACTWEAVE_TOKEN_ALLOW_BASIC_AUTH=false"
    And the JFrog CLI is available
    When I run jf "c add pw --url {platform}/ --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "rt ping --server-id pw"
    Then the command succeeds
    And the output contains "OK"
    And the server answered 200 to POST "/artifactory/api/security/token"
    And the server never answered 401 to POST "/artifactory/api/security/token"
