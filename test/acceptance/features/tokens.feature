Feature: Access tokens

  Background:
    Given the registry is running

  Scenario: Create a scoped token and list it over HTTP
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"username":"svc","scope":"applied-permissions/user","expires_in":86400,"description":"ci"}
      """
    Then the response status is 200
    And the JSON field "token_type" equals "Bearer"
    And the JSON field "token_id" is not empty
    When I GET "/access/api/v1/tokens" as admin
    Then the response status is 200
    And the response body contains "ci"

  Scenario: jf atc mints a token that jf c add uses
    Given the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/tokens-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    # jf atc authenticates only with an access token, so the first one comes
    # from the API.
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "admin-token"
    When I run jf "atc --url {platform}/ --access-token {admin-token} --expiry 7200"
    Then the command succeeds
    And I keep the JSON field "access_token" of the output as "token"
    When I run jf "c add token-server --url {platform}/ --access-token {token} --interactive=false"
    Then the command succeeds
    When I run jf "rt ping --server-id token-server"
    Then the command succeeds
    And the output contains "OK"
    Given a file "hello.txt" with content:
      """
      hello from a token
      """
    When I run jf "rt u hello.txt tokens-local/ --server-id token-server"
    Then the command succeeds
    When I GET "/artifactory/tokens-local/hello.txt" as admin
    Then the response status is 200
    And the response body contains "hello from a token"
