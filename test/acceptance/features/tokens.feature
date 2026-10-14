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

  Scenario: jf refreshes an access token that has expired
    Given the JFrog CLI is available
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"refreshable":true,"expires_in":3}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "short"
    And I keep the JSON field "refresh_token" of the response as "refresh"
    When I run jf "c add refresh-server --url {platform}/ --access-token {short} --interactive=false"
    Then the command succeeds
    Given the jf server "refresh-server" has the refresh token kept as "refresh"
    And the access token kept as "short" has expired
    # The CLI refreshes before the request, sending the expired token as its
    # Bearer credential; the ping then answers with the new token, which an
    # expired one would not.
    When I run jf "rt ping --server-id refresh-server"
    Then the command succeeds
    And the output contains "OK"
    And the server never answered 401 to POST "/access/api/v1/tokens"
    # The refresh revoked the expired token, so it cannot refresh again.
    Given the next request has header "Authorization" "Bearer {short}"
    When I POST "/access/api/v1/tokens" with body:
      """
      {"grant_type":"refresh_token","refresh_token":"{refresh}"}
      """
    Then the response status is 401

  Scenario: jf refreshes an expired access token through the legacy route
    Given the JFrog CLI is available
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"refreshable":true,"expires_in":3}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "short"
    And I keep the JSON field "refresh_token" of the response as "refresh"
    When I import the jf server "legacy-server" with the access token kept as "short" and the refresh token kept as "refresh"
    Then the command succeeds
    Given the access token kept as "short" has expired
    # The CLI refreshes through the legacy route with no credentials and the
    # expired token as access_token, which must pair with the refresh token.
    When I run jf "rt ping --server-id legacy-server"
    Then the command succeeds
    And the output contains "OK"
    And the server answered 200 to POST "/artifactory/api/security/token"
    And the server never answered 401 to POST "/artifactory/api/security/token"
    # The refresh revoked the expired token, so it cannot refresh again.
    Given the next request has header "Content-Type" "application/x-www-form-urlencoded"
    When I POST "/artifactory/api/security/token" with body:
      """
      grant_type=refresh_token&access_token={short}&refresh_token={refresh}
      """
    Then the response status is 401
