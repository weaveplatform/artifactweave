@api_reliability
Feature: API retry safety and bounded identity pages
  Optional append keys and Access page limits are artifactweave extensions
  described in decision 0043. They do not claim captured JFrog behavior.

  Background:
    Given the registry is running

  Scenario: Repeating a protected append creates one visible effect
    When I PUT "/artifactory/api/build" as admin with body:
      """
      {"name":"retry-app","number":"1","started":"2026-10-15T09:30:00.000+0000","modules":[]}
      """
    Then the response status is 204
    Given the next request has idempotency key "acceptance-append"
    When I POST "/artifactory/api/build/append/retry-app/1" as admin with body:
      """
      [{"id":"once"}]
      """
    Then the response status is 204
    Given the next request has idempotency key "acceptance-append"
    When I POST "/artifactory/api/build/append/retry-app/1" as admin with body:
      """
      [{"id":"once"}]
      """
    Then the response status is 204
    When I GET "/artifactory/api/build/retry-app/1" as admin
    Then the JSON field "buildInfo.modules" has 1 entries
    Given the next request has idempotency key "acceptance-append"
    When I POST "/artifactory/api/build/append/retry-app/1" as admin with body:
      """
      [{"id":"different"}]
      """
    Then the response status is 422

  Scenario: Unsupported token issuance cannot silently ignore a key
    Given the next request has idempotency key "unsupported-token"
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 400

  Scenario: Identity pages honor a small limit and reject oversized requests
    When I POST "/access/api/v2/users" as admin with body:
      """
      {"username":"page-alice","email":"alice@example.com","password":"Alice-pass1"}
      """
    Then the response status is 201
    When I POST "/access/api/v2/users" as admin with body:
      """
      {"username":"page-bob","email":"bob@example.com","password":"Bobby-pass1"}
      """
    Then the response status is 201
    When I GET "/access/api/v2/users?username=page-&limit=1" as admin
    Then the response status is 200
    And the JSON field "users" has 1 entries
    And the JSON field "users.0.username" equals "page-alice"
    And I keep the JSON field "cursor" of the response as "identity-cursor"
    When I GET "/access/api/v2/users?username=page-&limit=1&cursor={identity-cursor}" as admin
    Then the response status is 200
    And the JSON field "users.0.username" equals "page-bob"
    When I GET "/access/api/v2/users?limit=1001" as admin
    Then the response status is 400
    When I GET "/access/api/v2/groups?limit=1001" as admin
    Then the response status is 400
